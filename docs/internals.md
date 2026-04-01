# go-ratelimiter — Internals & Design Notes

This document explains the implementation decisions, data structures, and
algorithmic details behind each rate-limiter in this library.

---

## Table of Contents

1. [Shared design principles](#shared-design-principles)
2. [Token Bucket](#token-bucket)
3. [Sliding Window Counter](#sliding-window-counter)
4. [Leaky Bucket](#leaky-bucket)
5. [Concurrency model](#concurrency-model)
6. [Benchmarks & complexity](#benchmarks--complexity)
7. [Choosing the right limiter](#choosing-the-right-limiter)

---

## Shared design principles

### Zero background goroutines

Every refill and eviction is performed **lazily** — only when `Allow()` or
`AllowN()` is called.  This avoids the overhead and non-determinism of a
background ticker and means the library is safe to use in test environments
with fake clocks.

### Monotonic time

All time measurements use `time.Now()`, which in Go 1.9+ reads the
monotonic clock component when comparing two `time.Time` values with
`.Sub()`.  This prevents the limiters from misbehaving across daylight-saving
transitions or NTP adjustments.

### Single mutex per limiter

Each limiter holds exactly one `sync.Mutex`.  All exported methods acquire
the mutex for the duration of their work.  Read-only helpers (`Tokens()`,
`Count()`, `QueueDepth()`) also hold the mutex so callers always see a
consistent snapshot.

---

## Token Bucket

### State

```go
type TokenBucket struct {
    mu       sync.Mutex
    capacity int
    rate     float64   // tokens per second
    tokens   float64   // current fill level
    lastTime time.Time // last refill timestamp
}
```

### Refill on demand

On each call to `Allow()` / `AllowN(n)` the implementation computes:

```
elapsed  = now - lastTime
newTokens = elapsed.Seconds() * rate
tokens   = min(tokens + newTokens, capacity)
lastTime = now
```

This is a standard **lazy token bucket**.  No goroutine is needed because the
full "what would have accumulated" math is done in O(1) arithmetic on the
call path.

### Burst behaviour

The bucket allows a burst of up to `capacity` tokens instantly.  After a
burst the bucket is empty and callers are throttled until enough real time
passes for tokens to accumulate.

### AllowN atomicity

`AllowN(n)` refills first, then checks `tokens >= n` under the same lock
acquisition.  This guarantees that two concurrent callers cannot each
"see" enough tokens and both succeed when only one should.

---

## Sliding Window Counter

### State

```go
type SlidingWindow struct {
    mu         sync.Mutex
    limit      int
    window     time.Duration
    timestamps []time.Time // ring of request timestamps
}
```

### Eviction

On each `Allow()` call, all timestamps older than `now - window` are removed
from the front of the slice.  This is an O(k) operation where k is the
number of expired entries; in the steady state k ≈ 1.

```
cutoff = now - window
evict all ts in timestamps where ts < cutoff
if len(timestamps) < limit:
    append now
    return true
return false
```

### Memory usage

The slice holds at most `limit` timestamps, so memory is O(limit).  Expired
entries are evicted eagerly, so the slice rarely reaches its maximum size in
practice.

### Why not a fixed window?

A fixed window counter (e.g., "100 req/minute resetting at :00 and :60") has
a well-known **boundary burst** problem: a client can send 100 requests at
:59 and another 100 at :01 — 200 requests in two seconds.  The sliding window
eliminates this by evaluating the limit over a rolling interval ending **now**.

---

## Leaky Bucket

### State

```go
type LeakyBucket struct {
    mu        sync.Mutex
    capacity  int
    rate      float64   // requests drained per second
    queue     int       // current occupancy
    lastDrain time.Time
}
```

### Drain on demand

Similar to the token bucket, draining is lazy:

```
elapsed = now - lastDrain
drained = floor(elapsed.Seconds() * rate)
queue   = max(0, queue - drained)
if drained > 0: lastDrain = now
```

Note: `lastDrain` is only advanced when at least one request was drained.
This avoids floating-point accumulation error when the elapsed time is
shorter than one drain interval.

### Admission

A request is admitted if the queue is not full after draining:

```
if queue < capacity:
    queue++
    return true
return false   // drop
```

### Leaky bucket vs token bucket

| Property          | Token Bucket          | Leaky Bucket          |
|-------------------|-----------------------|-----------------------|
| Output rate       | Bursty up to capacity | Constant              |
| Burst storage     | Token count           | Queue depth           |
| Excess requests   | Rejected immediately  | Rejected (no queuing) |
| Typical use-case  | API rate limiting     | Traffic shaping       |

This implementation **drops** excess requests rather than blocking.  A
blocking variant would require holding the mutex across a sleep, which
would prevent other goroutines from making progress — contrary to the
zero-goroutine design goal.

---

## Concurrency model

All three types use a **single coarse-grained mutex** rather than atomics or
fine-grained locking.  This choice was deliberate:

- The critical section is extremely short (a few arithmetic operations).
- Mutex contention benchmarks show < 5 % overhead vs atomic CAS at the
  request rates these limiters are designed for (≤ 100 k req/s per instance).
- Coarse locking keeps the code simple and easy to audit for correctness.

For extremely high-throughput workloads (millions of req/s) consider sharding
by request key across multiple limiter instances.

---

## Benchmarks & complexity

Run with `go test -bench=. -benchmem ./...`:

| Benchmark              | ns/op | allocs/op | Notes                        |
|------------------------|-------|-----------|------------------------------|
| TokenBucket/Allow      |  ~45  |     0     | No allocations               |
| TokenBucket/AllowN-8   |  ~47  |     0     |                              |
| SlidingWindow/Allow    |  ~90  |     1     | One `time.Time` append        |
| SlidingWindow/Evict-64 | ~180  |     0     | Evicts 64 expired timestamps |
| LeakyBucket/Allow      |  ~50  |     0     | No allocations               |

_Numbers measured on Apple M2, Go 1.22, single goroutine._

---

## Choosing the right limiter

```
Need to allow short bursts?
  └─ YES → Token Bucket
Need strict per-interval request count?
  └─ YES → Sliding Window Counter
Need to enforce a constant output rate (traffic shaping)?
  └─ YES → Leaky Bucket
```

When in doubt, start with **Token Bucket** — it is the most common choice for
API rate limiting, handles bursty clients gracefully, and has the best
performance characteristics (O(1), zero allocations).
