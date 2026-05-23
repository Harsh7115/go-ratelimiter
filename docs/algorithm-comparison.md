# Rate Limiter Algorithm Comparison

This document compares the three rate-limiting algorithms implemented in this library — **Token Bucket**, **Sliding Window**, and **Leaky Bucket** — so you can choose the right one for your use case.

---

## Quick Reference

| Property              | Token Bucket        | Sliding Window Log  | Leaky Bucket        |
|-----------------------|---------------------|---------------------|---------------------|
| Burst traffic allowed | ✅ Yes              | ✅ Configurable     | ❌ No               |
| Memory per client     | O(1)                | O(burst size)       | O(1)                |
| Smoothing output      | Partial             | Partial             | ✅ Strict           |
| Clock drift sensitive | Low                 | Medium              | Low                 |
| Fairness              | Good                | Excellent           | Excellent           |
| Typical use case      | APIs, RPC           | User-facing APIs    | Egress, streaming   |

---

## Token Bucket

### How It Works

A bucket holds up to `capacity` tokens. Tokens are added at a fixed `refillRate` (tokens/second). Each request consumes one token. If the bucket is empty the request is denied.

```
tokens = min(capacity, tokens + refillRate * elapsed)
if tokens >= 1:
    tokens -= 1
    allow
else:
    deny
```

### Characteristics

- **Bursts**: a fully-charged bucket lets `capacity` requests through instantly — great for bursty-but-bounded traffic.
- **State**: two atomic values (`tokens`, `lastRefill`) — cheapest possible per-client state.
- **Weakness**: a client can save tokens and fire a large burst, which may overload downstream services even if the average rate is within budget.

### When to Use

- You want to allow short bursts (e.g., a mobile client syncing after being offline).
- You need minimal per-client memory with many clients.
- API gateways where a burst penalty is acceptable.

### Go usage

```go
limiter := ratelimiter.NewTokenBucket(10, 5) // capacity=10, refill=5/s
if limiter.Allow() {
    // serve request
}
```

---

## Sliding Window

### How It Works

Each request is timestamped and stored. When a new request arrives, all timestamps older than `windowSize` are evicted. If the remaining count is below `limit`, the request is allowed.

```
evict timestamps older than (now - windowSize)
if len(timestamps) < limit:
    timestamps.append(now)
    allow
else:
    deny
```

### Characteristics

- **Accuracy**: no boundary artifacts — a request at t=0.9s and one at t=1.1s both count against the same window correctly.
- **State**: O(n) where n = max allowed requests per window — fine for small limits, heavy for large ones.
- **Weakness**: memory grows with the limit value; eviction has O(n) worst case per call.

### When to Use

- You need precise per-user rate limiting (e.g., "100 API calls per 15 minutes per user").
- Fairness matters more than memory efficiency.
- Limit values are small (< a few hundred per window).

### Go usage

```go
limiter := ratelimiter.NewSlidingWindow(100, time.Minute) // 100 req/min
if limiter.Allow() {
    // serve request
}
```

---

## Leaky Bucket

### How It Works

Requests enter a FIFO queue (the "bucket"). A background drain loop processes one request every `1/drainRate` seconds. If the queue is full, new requests are rejected.

```
if len(queue) < capacity:
    queue.enqueue(request)
    allow (will be processed at fixed rate)
else:
    deny (bucket full)
```

### Characteristics

- **Smoothing**: output is perfectly regular — no bursts downstream.
- **State**: O(capacity) for the queue plus one ticker goroutine.
- **Weakness**: adds latency (requests wait in queue). Not suitable when the caller needs an immediate yes/no without queuing.

### When to Use

- You need a constant, predictable outbound rate (e.g., calling a third-party API with a strict TPS limit).
- Downstream services cannot handle spikes.
- Traffic shaping on egress (upload bandwidth, event streaming).

### Go usage

```go
limiter := ratelimiter.NewLeakyBucket(50, 10) // capacity=50, drain=10/s
if limiter.Allow() {
    // request queued, will drain at 10/s
}
defer limiter.Stop()
```

---

## Choosing the Right Algorithm

```
Need strict output rate / shape egress?
  └─ Yes → Leaky Bucket

Need to allow short bursts with low memory overhead?
  └─ Yes → Token Bucket

Need precise per-window fairness, bursts acceptable?
  └─ Yes → Sliding Window
```

### Combined strategies

For high-traffic services you can **chain** limiters:

```go
// Global: leaky bucket for egress smoothing
// Per-user: token bucket for burst allowance
global := ratelimiter.NewLeakyBucket(1000, 200)
perUser := ratelimiter.NewTokenBucket(20, 5)

if perUser.Allow() && global.Allow() {
    handle(req)
}
```

---

## Benchmark Results (Apple M2, Go 1.22)

| Algorithm      | Allow() ns/op | Allocs/op |
|----------------|--------------|-----------|
| Token Bucket   | ~45 ns       | 0         |
| Sliding Window | ~180 ns      | 1         |
| Leaky Bucket   | ~60 ns       | 0         |

Run benchmarks yourself:

```bash
go test ./... -bench=. -benchmem
```

---

## Further Reading

- [Token Bucket — Wikipedia](https://en.wikipedia.org/wiki/Token_bucket)
- [Leaky Bucket — Wikipedia](https://en.wikipedia.org/wiki/Leaky_bucket)
- [Cloudflare Blog: How we built rate limiting](https://blog.cloudflare.com/counting-things-a-lot-of-different-things/)
