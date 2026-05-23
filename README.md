# go-ratelimiter

Thread-safe rate limiting algorithms implemented in Go with **zero external dependencies**.

## Algorithms

### Token Bucket
Allows short bursts up to capacity, then refills at a steady rate. Ideal for APIs that permit occasional spikes.

```go
limiter := ratelimiter.NewTokenBucket(100, time.Second) // 100 req/s, burst allowed
if limiter.Allow() {
    // handle request
}
```

### Sliding Window
Tracks exact request timestamps over a rolling window — no burst allowance, strict rate enforcement.

```go
limiter := ratelimiter.NewSlidingWindow(100, time.Second) // exactly 100 req/s
if limiter.Allow() {
    // handle request
}
```

### Leaky Bucket
Processes requests at a constant output rate regardless of input bursts. Good for smoothing traffic.

```go
limiter := ratelimiter.NewLeakyBucket(100, time.Second) // drains 100 req/s
if limiter.Allow() {
    // handle request
}
```

## Install

```bash
go get github.com/Harsh7115/go-ratelimiter
```

## Algorithm Comparison

| Algorithm | Burst Handling | Memory | Use Case |
|-----------|---------------|--------|----------|
| Token Bucket | ✅ Yes | O(1) | APIs with allowed bursts |
| Sliding Window | ❌ No | O(n) | Strict per-window limits |
| Leaky Bucket | ✅ Smoothed | O(1) | Traffic shaping |

## Design

All limiters implement a common `Limiter` interface:

```go
type Limiter interface {
    Allow() bool
    AllowN(n int) bool
    Reset()
}
```

Thread safety is guaranteed via `sync.Mutex` — safe for concurrent use across goroutines.

## Tech Stack

Go · `sync` · `time`

---

Built to understand the trade-offs between rate limiting strategies at the systems level.
