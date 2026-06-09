# go-ratelimiter

[![Go Reference](https://pkg.go.dev/badge/github.com/Harsh7115/go-ratelimiter.svg)](https://pkg.go.dev/github.com/Harsh7115/go-ratelimiter)
[![Go Report Card](https://goreportcard.com/badge/github.com/Harsh7115/go-ratelimiter)](https://goreportcard.com/report/github.com/Harsh7115/go-ratelimiter)
[![License: MIT](https://img.shields.io/badge/License-MIT-green)](LICENSE)

Token bucket, sliding window, and leaky bucket rate limiters in Go — thread-safe, zero dependencies.

## Install

```bash
go get github.com/Harsh7115/go-ratelimiter
```

## Algorithms

### Token Bucket

Allows bursts up to capacity, then refills at a steady rate. Best for APIs where brief spikes are acceptable.

```go
limiter := ratelimiter.NewTokenBucket(100, time.Second) // 100 req/s, burst to 100

if limiter.Allow() {
    // request allowed
}

// Consume N tokens atomically
if limiter.AllowN(5) {
    // batch of 5 allowed
}

// Blocking: wait until a token is available (context-aware)
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()
if err := limiter.Wait(ctx); err != nil {
    // timed out waiting for capacity
}
```

### Sliding Window

Tracks exact request counts over a rolling time window — no burst allowance, strict per-window limit.

```go
limiter := ratelimiter.NewSlidingWindow(100, time.Second) // exactly 100 req/s

if limiter.Allow() {
    // within the window limit
}
```

### Leaky Bucket

Processes requests at a constant output rate regardless of input spikes. Smooths bursty traffic.

```go
limiter := ratelimiter.NewLeakyBucket(100, time.Second) // drains at 100 req/s

if limiter.Allow() {
    // queued and allowed through
}
```

## Algorithm Comparison

| Algorithm | Burst Handling | Memory | Precision | Best For |
|---|---|---|---|---|
| Token Bucket | Yes — up to capacity | O(1) | Approximate | APIs allowing short bursts |
| Sliding Window | No — strict per window | O(n requests) | Exact | Hard rate limits (billing, quotas) |
| Leaky Bucket | Yes — smoothed output | O(1) | Approximate | Traffic shaping, downstream protection |

## Common Interface

All three implement `Limiter`:

```go
type Limiter interface {
    Allow() bool              // non-blocking: returns false if over limit
    AllowN(n int) bool        // consume N tokens atomically
    Wait(ctx context.Context) error  // blocking: waits until allowed or ctx cancels
    Reset()                   // clear state, start fresh
}
```

## HTTP Middleware Example

```go
func RateLimitMiddleware(limiter ratelimiter.Limiter, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !limiter.Allow() {
            http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}

// Usage
limiter := ratelimiter.NewTokenBucket(1000, time.Second)
http.Handle("/api/", RateLimitMiddleware(limiter, apiHandler))
```

## Benchmarks

```
BenchmarkTokenBucket_Allow-8       ~82 ns/op    0 B/op    0 allocs/op
BenchmarkSlidingWindow_Allow-8    ~210 ns/op   48 B/op    1 allocs/op
BenchmarkLeakyBucket_Allow-8       ~90 ns/op    0 B/op    0 allocs/op
```

Token bucket and leaky bucket are O(1) with no allocations. Sliding window allocates one timestamp per request.

## Thread Safety

All limiters use `sync.Mutex` internally and are safe for concurrent use across goroutines. No external synchronization needed.

## Tech Stack

Go · `sync` · `time` · `context`

---

Built to understand the trade-offs between rate limiting strategies at the systems level.
