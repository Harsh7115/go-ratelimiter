# Architecture — go-ratelimiter

> This document describes the internal design of the three rate-limiting algorithms provided by this library: **Token Bucket**, **Sliding Window**, and **Leaky Bucket**.

---

## Overview

`go-ratelimiter` exposes a single `Limiter` interface so callers can swap algorithms without changing their request-handling code:

```go
type Limiter interface {
    Allow() bool          // non-blocking: returns true if the request is permitted
    Wait(ctx context.Context) error // blocking: waits until a token is available or ctx expires
    Close()               // releases background goroutines / timers
}
```

All three implementations are **thread-safe** (protected by a `sync.Mutex` or atomic operations) and have **zero external dependencies**.

---

## 1. Token Bucket

### Concept

A bucket holds up to `capacity` tokens. Tokens are added at a fixed `rate` (tokens per second). Each allowed request consumes one token. Requests that arrive when the bucket is empty are rejected (or blocked until a token is available).

### Internal state

```
TokenBucket {
    capacity  float64   // maximum tokens the bucket can hold
    tokens    float64   // current token count (float for sub-second precision)
    rate      float64   // tokens added per second
    lastCheck time.Time // timestamp of the last Allow() call
    mu        sync.Mutex
}
```

### Allow() algorithm

```
elapsed  = now - lastCheck
tokens  += elapsed.Seconds() * rate
tokens   = min(tokens, capacity)  // cap at capacity
lastCheck = now

if tokens >= 1.0 {
    tokens -= 1.0
    return true
}
return false
```

The key insight is **lazy refill**: instead of a background goroutine adding tokens every tick, we compute how many tokens *should* have accumulated since the last call. This avoids goroutine overhead and keeps the implementation lock-step with real time.

### Burst behaviour

Setting `capacity > rate` allows bursting: if the bucket has been idle it accumulates tokens up to `capacity`, enabling a short burst of back-to-back requests before throttling kicks in.

---

## 2. Sliding Window Counter

### Concept

A sliding window tracks the number of requests in the *most recent* `window` duration. Unlike a fixed window (which resets sharply at wall-clock boundaries and allows double-rate bursts at the boundary), the sliding window blends the current and previous window counts proportionally.

### Internal state

```
SlidingWindow {
    limit       int           // max requests per window
    window      time.Duration
    prevCount   int64         // request count in the previous full window
    currCount   int64         // request count in the current window so far
    windowStart time.Time     // start of the current window
    mu          sync.Mutex
}
```

### Allow() algorithm

```
elapsed  = now - windowStart
if elapsed >= window:
    prevCount   = currCount
    currCount   = 0
    windowStart = now
    elapsed     = 0

// how far through the current window are we? (0.0 – 1.0)
fraction = elapsed / window

// weighted count: old window contributes less as we move further in
weighted = prevCount * (1 - fraction) + currCount

if weighted + 1 <= limit:
    currCount++
    return true
return false
```

### Why blend windows?

A pure fixed window would let `2 * limit` requests through at the boundary (limit at the end of window N, limit at the start of window N+1). The blending factor `(1 - fraction)` gradually discounts the previous window as time passes, producing a smooth rate profile.

---

## 3. Leaky Bucket

### Concept

Requests enter a FIFO queue (the "bucket"). A background goroutine drains the queue at a fixed `rate`, forwarding one request every `1/rate` seconds. If the queue is full when a new request arrives, the request is dropped.

### Internal state

```
LeakyBucket {
    rate     float64       // requests drained per second
    capacity int           // max queue depth
    queue    chan struct{}  // buffered channel acts as the queue
    quit     chan struct{}  // signals the drainer to stop
}
```

### Drainer goroutine

```go
ticker := time.NewTicker(time.Duration(float64(time.Second) / rate))
for {
    select {
    case <-ticker.C:
        select {
        case <-queue: // drain one slot
        default:      // queue empty, nothing to do
        }
    case <-quit:
        ticker.Stop()
        return
    }
}
```

### Allow() / Wait()

- **Allow()**: attempts a non-blocking send on `queue`. Returns `true` if successful (slot available), `false` if the queue is full.
- **Wait()**: blocks on a send with context cancellation. Useful for back-pressure scenarios where callers should slow down rather than drop requests.

### Key difference from Token Bucket

Token Bucket permits **bursts** (multiple tokens can accumulate while idle). Leaky Bucket enforces a **constant outflow rate** regardless of idle periods — all requests, no matter how they arrive, exit the bucket at the same steady drip.

---

## Concurrency model

| Algorithm       | Synchronisation         | Background goroutines |
|-----------------|-------------------------|-----------------------|
| Token Bucket    | `sync.Mutex`            | none                  |
| Sliding Window  | `sync.Mutex`            | none                  |
| Leaky Bucket    | buffered channel + mutex | 1 (drainer)           |

Token Bucket and Sliding Window use a lazy-evaluation model: all state updates happen inside `Allow()`, guarded by a mutex. Leaky Bucket requires a drainer goroutine because it models *output* timing, not *input* budgeting.

---

## Choosing the right algorithm

| Requirement                                  | Recommended algorithm  |
|----------------------------------------------|------------------------|
| Allow occasional bursts, smooth average rate | Token Bucket           |
| Hard per-window limit, minimal burst         | Sliding Window Counter |
| Strict constant-rate output (e.g. API proxy) | Leaky Bucket           |

---

## Extending the library

To add a new algorithm:

1. Create a new file `<name>.go` in the package root.
2. Define a struct implementing `Limiter`.
3. Add a constructor `New<Name>(rate float64, ...) *<Name>`.
4. Add benchmark and unit tests in `<name>_test.go`.
5. Update this document.
