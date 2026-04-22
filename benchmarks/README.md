# Benchmarks

Performance numbers for the three rate-limiting algorithms across a range of workloads. All results were captured on an Apple M2 Pro (12 cores, 32 GB RAM) running Go 1.22.2 with `GOMAXPROCS=12`.

## Running the Benchmarks

```bash
# Full suite with race detector (catches data races; slightly slower)
go test -race -bench=. -benchmem -count=3 ./benchmarks/

# CPU profiling
go test -bench=BenchmarkTokenBucket -cpuprofile=cpu.prof ./benchmarks/
go tool pprof -http=:6060 cpu.prof

# Memory profiling
go test -bench=. -memprofile=mem.prof -benchmem ./benchmarks/
go tool pprof -http=:6060 mem.prof
```

---

## Results (April 2026)

### Single-goroutine throughput

| Algorithm | ops/sec | ns/op | B/op | allocs/op |
|-----------|--------:|------:|-----:|----------:|
| TokenBucket/Allow | 312,500,000 | 3.20 | 0 | 0 |
| TokenBucket/AllowN(10) | 298,000,000 | 3.35 | 0 | 0 |
| SlidingWindow/Allow | 48,200,000 | 20.75 | 24 | 1 |
| SlidingWindow/AllowN(10) | 9,800,000 | 102.0 | 240 | 10 |
| LeakyBucket/Allow | 285,000,000 | 3.51 | 0 | 0 |
| LeakyBucket/AllowN(10) | 271,000,000 | 3.69 | 0 | 0 |

### Concurrent throughput (12 goroutines)

| Algorithm | ops/sec | ns/op | Contention |
|-----------|--------:|------:|:-----------|
| TokenBucket/Parallel | 89,000,000 | 11.2 | Low — mutex held briefly |
| SlidingWindow/Parallel | 7,100,000 | 141 | Moderate — timestamp slice grows |
| LeakyBucket/Parallel | 81,500,000 | 12.3 | Low — mutex held briefly |

### Reset cost

| Algorithm | ns/op | B/op |
|-----------|------:|-----:|
| TokenBucket/Reset | 2.1 | 0 |
| SlidingWindow/Reset | 8.4 | 0 |
| LeakyBucket/Reset | 2.3 | 0 |

---

## Key Observations

### TokenBucket — fastest for burst-tolerant workloads
At **3.2 ns/op** single-threaded and **~11 ns/op** under heavy parallelism, the token bucket is the fastest of the three. The critical section is tiny: read the monotonic clock, compute elapsed tokens, compare and subtract. Zero heap allocations because all state lives in the struct.

The cost scales roughly linearly with goroutine count up to ~4 goroutines, then levels off as the mutex becomes the bottleneck. If you need higher throughput on many goroutines, shard by a consistent hash of the client key.

### SlidingWindow — accuracy at the cost of memory
The sliding window is **~6× slower** than token bucket because it maintains a timestamp ring-buffer. Each `Allow` call:
1. Takes the lock
2. Appends the current timestamp (1 allocation, 24 bytes)
3. Evicts expired entries from the front of the slice

Under high concurrency the eviction pass makes the critical section long enough to cause measurable contention. For strict burst control where accuracy matters more than raw throughput, the trade-off is worth it; otherwise prefer a token bucket.

**AllowN(n)** allocates n timestamps at once — this is intentional (atomic semantics) but means `AllowN` is O(n) in both time and memory.

### LeakyBucket — smooth output, near token-bucket speed
The leaky bucket uses lazy time-based drain: on each `Allow` call it computes how many requests have drained since the last call and adjusts the queue depth accordingly — no background goroutine, no timers. This keeps it at **3.5 ns/op** single-threaded with zero allocations, nearly matching the token bucket.

The key difference from a token bucket: the leaky bucket enforces a **constant output rate** and silently drops excess requests when the queue is full. Use it when you want smooth downstream traffic regardless of client burst patterns.

---

## Choosing an Algorithm

```
Need strict per-window fairness?     → SlidingWindow
Burst-tolerant, max throughput?      → TokenBucket
Constant downstream rate?            → LeakyBucket
```

For multi-tenant APIs (per-IP, per-user), wrap each algorithm instance in a `sync.Map`-backed registry (see `examples/per_client.go`). Evict stale entries with a periodic sweep to avoid unbounded memory growth.

---

## Benchmark Code

See [`benchmark_test.go`](benchmark_test.go) for the full benchmark suite. Key patterns:

```go
func BenchmarkTokenBucketAllow(b *testing.B) {
    tb := ratelimiter.NewTokenBucket(math.MaxInt32, math.MaxFloat64)
    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            tb.Allow()
        }
    })
}
```

`math.MaxInt32` capacity and `math.MaxFloat64` rate ensure the limiter never actually blocks during the benchmark — we're measuring the overhead of the fast path only. To benchmark the rejection path, set capacity to 0 and rate to 0.

---

*Benchmarks last updated: April 2026 — Go 1.22.2, Apple M2 Pro*
