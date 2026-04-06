# Rate-Limiting Algorithm Overview

This document explains the three algorithms in **go-ratelimiter**, their properties, trade-offs, and when to choose each one.

---

## 1. Token Bucket

### Mechanics

A token bucket holds up to **capacity** tokens. Tokens refill at a constant rate. Each request consumes one token; if the bucket is empty the request is rejected.

```
tokens(t) = min(capacity, tokens(t-1) + rate * delta_t)
allow = tokens > 0  =>  tokens--
```

### Properties

| | |
|---|---|
| Burst | Yes — up to `capacity` requests can fire instantly |
| Smoothness | Moderate — bursts absorbed, then output evens out |
| Memory | O(1) |

### When to use

- APIs that grant users a per-interval credit budget.
- Workloads with bursty-but-bounded traffic patterns.
- When you want to allow short bursts without raising the sustained rate ceiling.

```go
rl, _ := ratelimiter.NewTokenBucket(100, 10) // burst=100, refill=10 tok/sec
if rl.Allow() { handle() }
```

---

## 2. Sliding Window

### Mechanics

Tracks requests over a rolling time window using two counters (previous window and current window). The effective count is a weighted blend:

```
count = prev * (1 - elapsed/window) + curr
allow = count < limit
```

This is an O(1)-memory approximation of a true sliding log. It is exact at boundaries and has bounded error in between.

### Properties

| | |
|---|---|
| Burst | Limited — window boundary spikes are dampened |
| Smoothness | High — no abrupt resets |
| Memory | O(1) |

### When to use

- Per-user or per-IP limits where a uniform distribution is required.
- Compliance requirements that prohibit the boundary-doubling effect of fixed windows.
- Anywhere the fixed-window cliff causes observable quality-of-service problems.

```go
rl, _ := ratelimiter.NewSlidingWindow(100, time.Minute) // 100 req/min
if rl.Allow() { handle() }
```

---

## 3. Leaky Bucket

### Mechanics

Requests enter a finite queue; a background goroutine drains the queue at a constant rate. If the queue is full, the request is dropped.

```
enqueue(req)             // drop if queue full
drain goroutine: serve at drain_rate req/sec
```

### Properties

| | |
|---|---|
| Burst | Yes — queue buffers incoming spikes |
| Smoothness | Perfect — output is strictly constant |
| Memory | O(queue_size) |
| Latency added | Up to queue_size / drain_rate seconds |

### When to use

- Calling a downstream service with a hard per-second rate limit.
- Traffic shaping before an upstream that cannot tolerate bursts.
- When a constant output rate matters more than fast rejection.

```go
rl, _ := ratelimiter.NewLeakyBucket(10, 50) // 10 req/sec, queue cap=50
if rl.Allow() { handle() }
```

---

## Comparison at a Glance

| | Token Bucket | Sliding Window | Leaky Bucket |
|---|:---:|:---:|:---:|
| Allows bursts | YES | limited | YES (queued) |
| Constant output | NO | NO | YES |
| O(1) memory | YES | YES | NO |
| Extra latency | NO | NO | YES |
| Typical use | API credits | Strict period caps | Upstream metering |

---

## Thread Safety

All three implementations protect internal state with a `sync.Mutex` and are safe for concurrent use without any additional synchronisation.

---

## References

- RFC 2697 — A Single Rate Three Color Marker (token bucket variant)
- Cloudflare blog: *How we built rate limiting capable of scaling to millions of domains*
- Kleppmann, *Designing Data-Intensive Applications*, Ch. 11 (stream processing / back-pressure)
