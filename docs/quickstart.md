# Quickstart

This guide walks through the three rate limiters in the package and how to drop them into a Go service.

## Install

    go get github.com/Harsh7115/go-ratelimiter

The module has zero non-stdlib dependencies and targets Go 1.21+.

## Token bucket

The token bucket is the right choice when bursts are acceptable as long as the long-run rate stays bounded.

    import "github.com/Harsh7115/go-ratelimiter/tokenbucket"

    // 100 requests/sec sustained, burst up to 200.
    tb := tokenbucket.New(tokenbucket.Config{
        Rate:     100,
        Capacity: 200,
    })

    if !tb.Allow() {
        // reject
    }

Use `AllowN(k)` for weighted requests (e.g. expensive endpoints consume more tokens).

## Sliding window

The sliding window log gives the most accurate per-window enforcement at the cost of memory proportional to allowed events per window.

    import "github.com/Harsh7115/go-ratelimiter/slidingwindow"

    sw := slidingwindow.New(slidingwindow.Config{
        Window: time.Second,
        Limit:  100,
    })

For very high throughput, prefer the counter-based variant (`slidingwindow.NewCounter`), which uses a fixed amount of memory per key at the cost of small interpolation error around the window boundary.

## Leaky bucket

The leaky bucket smooths bursty input into a steady output stream — useful in front of a downstream service that cannot absorb spikes.

    import "github.com/Harsh7115/go-ratelimiter/leakybucket"

    lb := leakybucket.New(leakybucket.Config{
        Capacity: 1000,         // queue depth
        Leak:     200,          // per second
    })

    err := lb.Submit(ctx, request)

## Per-key limiting

All three limiters expose a `Keyed` constructor that maintains a separate state per string key (e.g. user ID, IP, API token):

    keyed := tokenbucket.NewKeyed(tokenbucket.Config{Rate: 10, Capacity: 20})
    if !keyed.Allow(userID) { ... }

Idle keys are evicted by an internal LRU after a configurable TTL so that long-running processes do not leak memory.

## Concurrency

Every exported method is safe for concurrent use without external locking. The implementations use atomic operations on hot paths and only fall back to a per-bucket mutex for refill arithmetic.

## When to pick which

- Allow short bursts but cap sustained throughput  -> token bucket
- Strict "no more than N per window"               -> sliding window log
- Smooth bursts into a paced downstream            -> leaky bucket

See `docs/comparison.md` for a side-by-side decision table.
