# AdaptiveLimiter — AIMD Rate Control

`AdaptiveLimiter` is a self-tuning rate limiter that automatically adjusts its
allowed rate based on observed success and failure signals.  It implements the
classic **Additive Increase / Multiplicative Decrease (AIMD)** algorithm — the
same feedback loop used by TCP congestion control.

---

## When to Use AdaptiveLimiter

Use `AdaptiveLimiter` when:

- The downstream service's capacity is **unknown or variable** (e.g., a shared
  database, a third-party API with unpublished rate limits, auto-scaling backends).
- You want the limiter to **back off automatically** when errors or timeouts spike,
  and ramp back up once the service recovers.
- You need **observability** — AdaptiveLimiter exposes current rate, success ratio,
  and adjustment history via `Stats()`.

Use a static limiter (TokenBucket, SlidingWindow, LeakyBucket) when the limit is
known and stable.

---

## Algorithm: AIMD

AIMD operates on a **current rate** `r` that floats between `MinRate` and
`MaxRate`:

```
on success:
    r += AdditiveIncrease          // e.g. +1 req/s every probe window

on failure / timeout:
    r *= (1 - MultiplicativeDecrease)   // e.g. r = r * 0.75
    r = max(r, MinRate)
```

The rate is re-evaluated each **ProbeWindow** (default 1 second).  Within a
window, the limiter counts successes and failures via `ReportSuccess()` and
`ReportFailure()`.  At window boundary the AIMD step fires and the underlying
TokenBucket is reconfigured atomically.

### Why AIMD?

| Property | AIMD | PID controller |
|---|---|---|
| Convergence to fair share | ✓ (proven) | Depends on tuning |
| Oscillation | Low | Can be high |
| Implementation complexity | Simple | Moderate |
| Requires error derivative | No | Yes |

AIMD is conservative on decrease and aggressive on increase, making it well-suited
for rate-limit scenarios where over-shooting is more damaging than under-shooting.

---

## API

```go
// NewAdaptiveLimiter creates an AdaptiveLimiter.
//
//   minRate  — floor rate (req/s); never drops below this
//   maxRate  — ceiling rate (req/s); never exceeds this
//   initial  — starting rate
//   opts     — functional options (see below)
func NewAdaptiveLimiter(minRate, maxRate, initial float64, opts ...AdaptiveOption) *AdaptiveLimiter

// Allow returns true if a request is permitted at the current rate.
func (a *AdaptiveLimiter) Allow() bool

// AllowN returns true if n requests are permitted atomically.
func (a *AdaptiveLimiter) AllowN(n int) bool

// ReportSuccess signals that the last request(s) succeeded.
// Call once per successful downstream response.
func (a *AdaptiveLimiter) ReportSuccess()

// ReportFailure signals a downstream failure (error, timeout, 429, 503, …).
// Triggers the multiplicative decrease at the next window boundary.
func (a *AdaptiveLimiter) ReportFailure()

// Stats returns a snapshot of the current limiter state.
func (a *AdaptiveLimiter) Stats() AdaptiveStats

// Reset restores the limiter to its initial rate.
func (a *AdaptiveLimiter) Reset()
```

### Options

```go
// WithAdditiveIncrease sets the per-window increase step (default: 1.0 req/s).
func WithAdditiveIncrease(delta float64) AdaptiveOption

// WithMultiplicativeDecrease sets the decrease factor in (0, 1) (default: 0.25).
// On failure: newRate = oldRate * (1 - factor).
func WithMultiplicativeDecrease(factor float64) AdaptiveOption

// WithProbeWindow sets the evaluation window duration (default: 1s).
func WithProbeWindow(d time.Duration) AdaptiveOption

// WithFailureThreshold sets the minimum failure ratio [0,1] that triggers a
// decrease within a window (default: 0.1 — any failures trigger backoff).
func WithFailureThreshold(ratio float64) AdaptiveOption
```

### AdaptiveStats

```go
type AdaptiveStats struct {
    CurrentRate      float64       // allowed req/s right now
    SuccessCount     int64         // successes in current window
    FailureCount     int64         // failures in current window
    TotalIncreases   int64         // lifetime additive-increase steps
    TotalDecreases   int64         // lifetime multiplicative-decrease steps
    LastAdjustedAt   time.Time     // time of most recent rate change
}
```

---

## Usage Example

```go
package main

import (
    "context"
    "fmt"
    "math/rand"
    "time"

    rl "github.com/Harsh7115/go-ratelimiter"
)

func callDownstream() error {
    // Simulate 10% failure rate
    if rand.Float64() < 0.10 {
        return fmt.Errorf("downstream timeout")
    }
    return nil
}

func main() {
    limiter := rl.NewAdaptiveLimiter(
        5,   // min  5 req/s
        100, // max 100 req/s
        20,  // start at 20 req/s
        rl.WithAdditiveIncrease(2),
        rl.WithMultiplicativeDecrease(0.3),
        rl.WithProbeWindow(500*time.Millisecond),
    )

    ticker := time.NewTicker(10 * time.Millisecond) // attempt 100 req/s
    defer ticker.Stop()

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    for {
        select {
        case <-ctx.Done():
            s := limiter.Stats()
            fmt.Printf("Final rate: %.1f req/s | increases: %d | decreases: %d\n",
                s.CurrentRate, s.TotalIncreases, s.TotalDecreases)
            return
        case <-ticker.C:
            if !limiter.Allow() {
                continue // throttled
            }
            if err := callDownstream(); err != nil {
                limiter.ReportFailure()
            } else {
                limiter.ReportSuccess()
            }
        }
    }
}
```

---

## Tuning Guide

### AdditiveIncrease

Controls how fast the rate ramps up after a quiet period.  Higher values
recover faster but overshoot more before the next decrease.

- Latency-sensitive API (tight SLO): 0.5–1.0
- Background job, bulk loader: 5.0–10.0

### MultiplicativeDecrease

Controls how aggressively the limiter backs off on failure.  Higher factors
mean slower recovery but less pressure on an already-struggling service.

- Transient errors (network blip): 0.1–0.2
- Hard capacity limit (database max_connections): 0.3–0.5

### ProbeWindow

Shorter windows react faster but are noisier.  Match to the typical round-trip
latency of the downstream service.

- Sub-millisecond in-process: 50–200 ms
- Remote microservice: 500 ms–2 s
- Third-party API with unknown quota: 5–10 s

### FailureThreshold

Set to `0` to trigger backoff on any single failure.  Set to `0.05` to allow
up to 5% errors per window before backing off (useful for services with inherent
noise).

---

## Thread Safety

All methods are safe for concurrent use.  Internally, `AdaptiveLimiter` wraps a
`TokenBucket` and uses a single `sync.Mutex` to protect AIMD state.

---

## Comparison with Static Limiters

| | TokenBucket | SlidingWindow | AdaptiveLimiter |
|---|---|---|---|
| Knows the limit upfront | Required | Required | Optional |
| Handles unknown capacity | ✗ | ✗ | ✓ |
| Burst support | ✓ | Partial | ✓ (inherits from TokenBucket) |
| Automatic backoff | ✗ | ✗ | ✓ |
| Observability | Basic | Basic | Rich (Stats) |
| Overhead per request | O(1) | O(1) amortized | O(1) |

---

## References

- Chiu, D.-M., & Jain, R. (1989). Analysis of the increase and decrease algorithms
  for congestion avoidance in computer networks. *Computer Networks*, 17(1), 1–14.
- TCP congestion control (RFC 5681) — the canonical AIMD application.
- Netflix Concurrency Limits library — similar adaptive approach for gRPC.
