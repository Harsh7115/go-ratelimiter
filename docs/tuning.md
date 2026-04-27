# Tuning Guide

This document is for someone who has decided to add rate limiting to a
Go service and now needs to pick (a) which limiter and (b) what
numbers to plug into it. The README gives the API; this document gives
the *reasoning*.

## Step 1 — Decide what "request" means

Before tuning anything, pin down the unit of work being limited:

* **Per route** — one limiter per HTTP path. Cheapest. Useful when one
  endpoint is expensive and the rest are not.
* **Per caller** — one limiter per (API key | user ID | client IP).
  This is what most public APIs want. Memory cost grows with active
  caller count; see "Memory budget" below.
* **Per (caller, route)** — granular but expensive. Only worth it if
  callers can hammer one route legitimately while another route stays
  idle.
* **Global** — one limiter for the whole service. Useful as a safety
  net behind a per-caller limit; rarely the right *primary* limiter.

The choice changes the keying strategy, not the algorithm.

## Step 2 — Pick the algorithm

| Workload                               | Recommended           |
|----------------------------------------|-----------------------|
| Bursty traffic, want short bursts OK   | Token bucket          |
| Hard cap "N per minute", no smoothing  | Sliding window        |
| Constant downstream rate, no bursts    | Leaky bucket          |
| Anti-abuse on auth/login endpoints     | Sliding window        |
| Outbound to a 3rd-party API w/ a quota | Leaky bucket          |
| User-facing read API                   | Token bucket          |

Heuristics:

* **Token bucket** is the right default for inbound traffic. It is
  cheap, supports bursts, and is easy to explain to API consumers
  ("you get N requests per second, and we let you save up M of them").
* **Sliding window** when the SLA is phrased as "no more than N in
  any T-second window". Token bucket *approximates* this but does
  not enforce it strictly under adversarial timing.
* **Leaky bucket** when the *downstream* has a fixed-rate constraint
  and you cannot afford to send a burst even if your bucket allows it.
  The classic example is calling a partner API that is itself rate
  limited.

## Step 3 — Choose parameters

### Token bucket

```
rate     = sustained allowed rate, requests per second
capacity = burst size, requests
```

Defaults that work for most user-facing APIs:

* `rate` = expected steady-state load × 1.5
* `capacity` = `rate × 5` (5 seconds of saved-up budget)

Pick `capacity` from the user's perspective: how many requests should
they be able to fire back-to-back after a quiet period? Five seconds
of headroom is plenty for retries and parallel fanout without letting
buggy clients flood the backend.

### Sliding window

```
limit  = N requests
window = T seconds
```

There is no burst parameter — the limiter *is* the cap. Pick
`(limit, window)` directly from the SLA. Smaller windows are stricter
but cost more memory (one timestamp per request kept inside the
window). For high-volume endpoints, prefer a larger window: the
constants are smaller for one decision per million requests than for
one decision per thousand.

### Leaky bucket

```
leak_rate = requests per second drained
capacity  = max queued requests
```

`leak_rate` should match the *downstream* constraint, not the inbound
load. `capacity` controls the worst-case queue latency:

```
worst_case_latency_seconds = capacity / leak_rate
```

Default `capacity` to `leak_rate × 1` (one second of queue) unless
you have a specific latency budget in mind.

## Memory budget

Per-caller limiters cost roughly:

| Algorithm        | Bytes per active caller   |
|------------------|---------------------------|
| Token bucket     | ~64 (counter + timestamp) |
| Leaky bucket     | ~64 (counter + timestamp) |
| Sliding window   | 16 × N (one timestamp per in-window request) |

So a sliding window of 1000 requests/min costs ~16 KB per caller; a
service with a million distinct callers per day will need 16 GB
*assuming every caller is at the cap*. Typical workloads are nowhere
near that, but the math is worth doing once.

Use `Janitor(ttl)` to evict per-caller limiters that have been idle
longer than `ttl`. `ttl = 10 × window` is a safe default — the
limiter will be re-created on the caller's next request and will
correctly start at full budget.

## Common mistakes

* **Limiter on the wrong side of the load balancer.** A per-instance
  limiter set to `rate=100` on a fleet of 4 servers actually permits
  400 RPS. Either set the per-instance rate to `global / N` or use a
  shared backend (Redis, etc.).
* **Treating `Allow()` failure as fatal.** It isn't — surface a
  `429` with `Retry-After` and let clients back off. The default
  middleware does this; custom integrations sometimes don't.
* **Per-IP limits behind a CDN or NAT.** All your traffic appears to
  come from a handful of IPs. Limit on a header your CDN sets
  (`X-Forwarded-For`, `CF-Connecting-IP`, …) and validate that the
  upstream is trusted.
* **Forgetting clock skew.** Sliding window in particular is sensitive
  to wall-clock jumps. Use `time.Now()` everywhere; never accept
  client-supplied timestamps as the limiter's clock.

## Where to start

If unsure, this is a fine first configuration for a public read API:

```go
limiter := ratelimiter.NewTokenBucket(
    ratelimiter.WithRate(20),         // 20 RPS sustained
    ratelimiter.WithCapacity(100),    // 5 seconds of burst
    ratelimiter.WithKeyFunc(byAPIKey),
    ratelimiter.WithJanitor(10 * time.Minute),
)
```

Measure 95th-percentile actual usage in production, then tune from
there. Tuning before you have data is mostly cargo-culting.
