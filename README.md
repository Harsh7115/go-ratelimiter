# go-ratelimiter

Thread-safe rate limiters for Go — **Token Bucket**, **Sliding Window**, and **Leaky Bucket** — with zero external dependencies.

## Algorithms

| Algorithm | Best For | Burst Handling | Memory |
|---|---|---|---|
| Token Bucket | APIs with short bursts | Allows up to capacity | O(1) |
| Sliding Window | Strict per-window limits | Smooths boundary bursts | O(n) requests |
| Leaky Bucket | Enforcing constant output rate | Queues or drops excess | O(1) |

## Installation

```bash
go get github.com/Harsh7115/go-ratelimiter
```

Requires Go 1.22+.

## Quick Start

### Token Bucket

Requests accumulate tokens at `rate` tokens/second; bursts up to `capacity`.

```go
import "github.com/Harsh7115/go-ratelimiter"

// 100 tokens capacity, refilling at 10 tokens/second
tb := ratelimiter.NewTokenBucket(100, 10)

if tb.Allow() {
    // handle request
}

// Consume multiple tokens atomically
if tb.AllowN(5) {
    // handle bulk request
}

fmt.Printf("%.1f tokens remaining\n", tb.Tokens())
```

### Sliding Window Counter

At most `limit` requests in any rolling `window` duration.

```go
// 100 requests per minute
sw := ratelimiter.NewSlidingWindow(100, time.Minute)

if sw.Allow() {
    // handle request
}

fmt.Printf("%d requests in current window\n", sw.Count())
```

### Leaky Bucket

Requests drain at a constant rate; bursts beyond capacity are dropped.

```go
// 50-request queue, draining at 10 requests/second
lb := ratelimiter.NewLeakyBucket(50, 10)

if lb.Allow() {
    // handle request
}

fmt.Printf("%d requests queued\n", lb.QueueDepth())
```

## API Reference

All three types implement the `Limiter` interface:

```go
type Limiter interface {
    Allow() bool        // consume 1 token / slot
    AllowN(n int) bool  // consume n tokens / slots atomically
    Reset()             // restore to initial state
}
```

### TokenBucket

| Method | Signature | Description |
|---|---|---|
| NewTokenBucket | `(capacity int, rate float64) *TokenBucket` | rate in tokens/second |
| Allow | `() bool` | consume 1 token |
| AllowN | `(n int) bool` | consume n tokens atomically |
| Tokens | `() float64` | current token count |
| Reset | `()` | refill to capacity |

### SlidingWindow

| Method | Signature | Description |
|---|---|---|
| NewSlidingWindow | `(limit int, window time.Duration) *SlidingWindow` | max requests per window |
| Allow | `() bool` | admit 1 request |
| AllowN | `(n int) bool` | admit n requests atomically |
| Count | `() int` | requests in current window |
| Reset | `()` | clear all timestamps |

### LeakyBucket

| Method | Signature | Description |
|---|---|---|
| NewLeakyBucket | `(capacity int, rate float64) *LeakyBucket` | rate in requests/second |
| Allow | `() bool` | enqueue 1 request |
| AllowN | `(n int) bool` | enqueue n requests atomically |
| QueueDepth | `() int` | current queue occupancy |
| Reset | `()` | drain queue |

## Running Tests

```bash
# all tests with race detector
go test -race -count=1 ./...

# benchmarks
go test -bench=. -benchmem ./...
```

## Design Notes

- **Zero dependencies** — only `sync` and `time` from the standard library
- **Thread-safe** — all methods are protected by `sync.Mutex`
- **Lazy evaluation** — TokenBucket and LeakyBucket compute elapsed time on each call (no background goroutines)
- **O(1) amortized** — TokenBucket and LeakyBucket; SlidingWindow is O(n) on eviction, bounded by window size

## License

MIT
