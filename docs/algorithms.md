# Rate Limiting Algorithms

This document explains the three rate limiting algorithms implemented in **go-ratelimiter**, their internal mechanics, trade-offs, and when to reach for each one.

---

## 1. Token Bucket

### How it works

A bucket holds up to **capacity** tokens. A refill ticker adds tokens at a steady rate. Each incoming request consumes one token; if the bucket is empty the request is rejected.

```
capacity = 10 tokens
refill   = 10 tokens / second

t=0.0s  bucket=[10]  req → allowed  bucket=[9]
t=0.1s  bucket=[9]   req → allowed  bucket=[8]
...
t=0.9s  bucket=[1]   req → allowed  bucket=[0]
t=1.0s  refill fires bucket=[10]
```

### Properties

| Property | Value |
|---|---|
| Burst | Yes — up to capacity |
| Memory | O(1) |
| Precision | Approximate (refill interval) |
| Clock sensitivity | Low |

### When to use

- REST APIs where occasional spikes are acceptable
- Any endpoint that should absorb short bursts without penalizing the client
- Default choice when unsure which algorithm to pick

### Example

```go
// 100 requests per second, burst allowed
limiter := ratelimiter.NewTokenBucket(100, time.Second)
if limiter.Allow() {
    // serve request
}
```

---

## 2. Sliding Window

### How it works

Every request timestamp is appended to a log. On each Allow() call, timestamps older than the window are evicted, and the remaining count is compared against the limit.

```
window = 1s, limit = 3

t=0.0s  log=[0.0]            count=1 → allowed
t=0.4s  log=[0.0, 0.4]       count=2 → allowed
t=0.8s  log=[0.0, 0.4, 0.8]  count=3 → allowed
t=1.1s  log=[0.4, 0.8, 1.1]  count=3 → allowed  (0.0 evicted)
t=1.2s  log=[0.4, 0.8, 1.1]  count=3 → REJECTED
```

### Properties

| Property | Value |
|---|---|
| Burst | No — strict per-window |
| Memory | O(n) where n = limit |
| Precision | Exact |
| Clock sensitivity | High |

### When to use

- Billing or regulatory APIs with hard per-second/minute caps
- Security-sensitive endpoints (login, password reset, 2FA)
- When you need to guarantee exactly N requests in any rolling window

### Example

```go
// Strictly 60 requests per minute, no burst
limiter := ratelimiter.NewSlidingWindow(60, time.Minute)
if limiter.Allow() {
    // serve request
}
```

---

## 3. Leaky Bucket

### How it works

Requests fill an internal queue. A drainer processes one request every window/rate interval, producing a constant output rate regardless of how bursty the input is.

```
rate = 5 req/s → drain every 200ms

t=0ms   burst of 3 reqs → queued
t=200ms drain → 1 served
t=400ms drain → 1 served
t=600ms drain → 1 served
```

### Properties

| Property | Value |
|---|---|
| Burst | Absorbed and smoothed |
| Memory | O(1) |
| Precision | Approximate |
| Clock sensitivity | Medium |

### When to use

- Outbound HTTP clients (avoid hammering downstream APIs)
- Webhook delivery pipelines needing constant throughput
- Any place where a steady output rate matters more than low latency

### Example

```go
// Deliver at most 5 webhooks per second regardless of spikes
limiter := ratelimiter.NewLeakyBucket(5, time.Second)
if limiter.Allow() {
    deliver(webhook)
}
```

---

## Comparison Table

| | Token Bucket | Sliding Window | Leaky Bucket |
|---|---|---|---|
| **Burst handling** | Allows bursts | No burst | Smoothed output |
| **Memory** | O(1) | O(n) | O(1) |
| **Precision** | Approximate | Exact | Approximate |
| **Primary use** | General APIs | Strict limits | Traffic shaping |

---

## Thread Safety

All three implementations guard their state with `sync.Mutex`, so a single limiter instance can be shared safely across goroutines without additional synchronisation.

## Running benchmarks

```bash
go test ./... -bench=. -benchmem -cpu 1,2,4,8
```
