# Algorithm Comparison: Token Bucket vs Sliding Window vs Leaky Bucket

`go-ratelimiter` implements three classic rate-limiting algorithms. This guide explains how each one works, their trade-offs, and when to reach for each.

---

## Quick Reference

| Property | Token Bucket | Sliding Window | Leaky Bucket |
|----------|-------------|----------------|--------------|
| Burst support | ✅ Yes (up to bucket capacity) | ⚠️ Partial (window smooths it) | ❌ No (strict output rate) |
| Memory per key | O(1) | O(window / granularity) | O(1) |
| Steady-state accuracy | ✅ Exact | ✅ Exact | ✅ Exact |
| Spike protection | ⚠️ Allows accumulated bursts | ✅ Strong | ✅ Strongest |
| Typical use case | APIs with burst allowance | Analytics, rolling quotas | Traffic shaping, queues |
| Complexity | Low | Medium | Low |

---

## Token Bucket

### How It Works

Imagine a bucket with a maximum capacity of *B* tokens. A refill goroutine adds tokens at rate *r* tokens/second. Each incoming request consumes one token. If the bucket is empty, the request is rejected (or queued).

```
tokens = min(capacity, tokens + rate * elapsed_time)

allow = tokens >= 1
if allow:
    tokens -= 1
```

### Key Properties

- **Bursts are permitted** up to the bucket capacity. If no requests arrived for 5 seconds and capacity is 100, the next request can immediately fire 100 requests.
- **Long-term rate** is still bounded by the refill rate.
- **Memory**: two values per key — token count and last refill timestamp.

### When to Use

- REST APIs where clients deserve some burst headroom (e.g. 10 req/s with up to 50 burst).
- Anything where occasional spikes are acceptable as long as the average rate is respected.
- Simple, low-overhead rate limiting where O(1) state is important.

### Example

```go
limiter := ratelimiter.NewTokenBucket(ratelimiter.Config{
    Rate:     10,   // tokens per second
    Capacity: 50,   // max burst
})

if !limiter.Allow("user:42") {
    http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
    return
}
```

---

## Sliding Window

### How It Works

The sliding window counters approach splits time into small sub-windows (buckets) and counts requests in each. The total count across the past *T* seconds is the current rate. Old sub-windows expire as time moves forward.

A more accurate variant — the sliding window log — records the exact timestamp of each request and counts how many timestamps fall within `[now - T, now]`.

```
count = sum of requests in all sub-windows within [now - T, now]
allow = count < limit
```

### Key Properties

- **No burst accumulation** — the window always reflects the last T seconds, so there's no "debt" that can be spent all at once.
- **Smooth enforcement** — rate spikes at the boundary between fixed windows (a classic problem with fixed windows) are eliminated.
- **Memory**: proportional to window size divided by sub-window granularity, or O(N) requests in the log variant.

### When to Use

- Rolling quotas: "no more than 1000 API calls in any 60-second window."
- Analytics and billing where you need precise per-period counts.
- Anywhere that the fixed-window "double burst" problem is unacceptable.

### Example

```go
limiter := ratelimiter.NewSlidingWindow(ratelimiter.Config{
    Rate:   100,              // requests per window
    Window: 60 * time.Second, // window duration
})

if !limiter.Allow("tenant:acme") {
    http.Error(w, "quota exceeded", http.StatusTooManyRequests)
    return
}
```

---

## Leaky Bucket

### How It Works

The leaky bucket models a queue with a fixed output rate. Requests enter the top of the bucket and drain at a constant rate from the bottom. If the bucket overflows (queue is full), new requests are dropped.

```
# On each request:
current_time = now()
drain = (current_time - last_check) * rate
water = max(0, water - drain)
last_check = current_time

if water + 1 > capacity:
    reject
else:
    water += 1
    allow
```

### Key Properties

- **Strictly constant output rate** — requests are processed at exactly *r* req/s regardless of input patterns.
- **No bursts allowed** — even if the bucket has been empty, you can't instantly process multiple requests.
- **Queue semantics** — can be used to smooth bursty input into steady output (traffic shaping) rather than just dropping excess requests.

### When to Use

- **Traffic shaping**: you want requests to flow at a steady rate to a downstream service (e.g. throttle outbound API calls to a third-party).
- **Queue-based systems**: model a bounded work queue with a fixed processing rate.
- **Strict SLA enforcement**: you need to guarantee no client ever exceeds R req/s even momentarily.

### Example

```go
limiter := ratelimiter.NewLeakyBucket(ratelimiter.Config{
    Rate:     5,    // requests per second drained
    Capacity: 20,   // max queue depth
})

if !limiter.Allow("service:payment-gateway") {
    http.Error(w, "downstream throttle", http.StatusTooManyRequests)
    return
}
```

---

## Head-to-Head Scenarios

### Scenario 1: Bursty API client

A client is idle for 10 seconds, then sends 80 requests in 1 second (limit: 10 req/s, capacity: 50).

| Algorithm | Result |
|-----------|--------|
| Token Bucket | Allows first 50, rejects next 30. ✅ Burst headroom used correctly. |
| Sliding Window | Allows first 10, rejects next 70. ✅ Strict 10/s regardless of idle period. |
| Leaky Bucket | Allows first ~10 (1s drain), rejects the rest. ✅ Constant output rate. |

**Winner for burst headroom**: Token Bucket.
**Winner for strict burst prevention**: Leaky Bucket / Sliding Window.

### Scenario 2: Sustained overload

Client sends 20 req/s continuously against a 10 req/s limit.

All three algorithms reject ~50% of requests. Behavior is identical at steady state.

### Scenario 3: Rolling quota (hourly billing)

"Each tenant may make 10,000 calls per hour."

| Algorithm | Result |
|-----------|--------|
| Token Bucket | Works, but a tenant could burst 10,000 calls at the start of the hour. |
| Sliding Window | Enforces exactly 10,000 in any 60-minute window — no double-burst. ✅ |
| Leaky Bucket | Enforces ~2.78 req/s constantly — no batch processing possible. ❌ |

**Winner for rolling quotas**: Sliding Window.

---

## Choosing the Right Algorithm

```
Need constant output rate (traffic shaping)?
  └─► Leaky Bucket

Need rolling window quota (no double-burst)?
  └─► Sliding Window

Need burst allowance on top of average rate?
  └─► Token Bucket
```

When in doubt, **Token Bucket** is the most common choice for API rate limiting because it naturally accommodates client burst patterns while still enforcing long-term averages.

---

## Performance Notes

Benchmark results on an Apple M2 (go test -bench . -benchtime=5s):

| Algorithm | ns/op | Allocs/op |
|-----------|-------|-----------|
| Token Bucket | ~85 | 0 |
| Sliding Window (counter) | ~140 | 0 |
| Sliding Window (log) | ~310 | 1 |
| Leaky Bucket | ~90 | 0 |

All algorithms are safe for concurrent use with no external locking overhead visible at the call site.

---

## Further Reading

- [Token Bucket — Wikipedia](https://en.wikipedia.org/wiki/Token_bucket)
- [Leaky Bucket — Wikipedia](https://en.wikipedia.org/wiki/Leaky_bucket)
- [Cloudflare: How We Built Rate Limiting Capable of Scaling to Millions of Domains](https://blog.cloudflare.com/counting-things-a-lot-of-different-things/)
- [Figma Engineering: An alternative approach to rate limiting](https://www.figma.com/blog/an-alternative-approach-to-rate-limiting/)
