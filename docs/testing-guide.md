# Testing Guide

This guide covers unit testing, integration testing, and property-based testing
strategies for code that uses go-ratelimiter.

---

## 1. Unit Testing with a Fake Clock

Rate limiters depend on wall-clock time, which makes tests flaky and slow if you
use `time.Sleep` or `time.Now` directly.  Inject a fake clock so tests run at
simulation speed.

```go
// fakeclock_test.go
package myapp_test

import (
	"sync"
	"time"
)

// FakeClock lets tests advance time deterministically.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }
func (fc *FakeClock) Now() time.Time           { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.now }
func (fc *FakeClock) Advance(d time.Duration)  { fc.mu.Lock(); defer fc.mu.Unlock(); fc.now = fc.now.Add(d) }
```

When using go-ratelimiter directly (no clock injection), call `Reset()` between
sub-tests to clear accumulated state:

```go
func TestTokenBucketResets(t *testing.T) {
	limiter := ratelimiter.NewTokenBucket(5, time.Second)

	for i := 0; i < 5; i++ {
		if !limiter.Allow() {
			t.Fatalf("expected allow on request %d", i+1)
		}
	}
	if limiter.Allow() {
		t.Fatal("expected deny when bucket is empty")
	}

	limiter.Reset()
	if !limiter.Allow() {
		t.Fatal("expected allow after reset")
	}
}
```

---

## 2. Table-Driven Tests

Use table-driven tests to cover each algorithm with the same harness:

```go
func TestAllLimitersBasic(t *testing.T) {
	cases := []struct {
		name    string
		limiter ratelimiter.Limiter
		rps     int
	}{
		{"token-bucket",    ratelimiter.NewTokenBucket(10, time.Second),    10},
		{"sliding-window",  ratelimiter.NewSlidingWindow(10, time.Second),  10},
		{"leaky-bucket",    ratelimiter.NewLeakyBucket(10, time.Second),    10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed := 0
			for i := 0; i < tc.rps*2; i++ {
				if tc.limiter.Allow() {
					allowed++
				}
			}
			// Allow tolerance: token/leaky bucket may permit up to rps
			if allowed > tc.rps+1 {
				t.Errorf("%s: allowed %d/%d requests, want <= %d",
					tc.name, allowed, tc.rps*2, tc.rps+1)
			}
		})
	}
}
```

---

## 3. Concurrency Tests

Rate limiters must be safe under concurrent access.  Use `-race` and a
goroutine storm to surface races:

```go
func TestTokenBucketConcurrent(t *testing.T) {
	const goroutines = 100
	const requestsEach = 50

	limiter := ratelimiter.NewTokenBucket(200, time.Second)
	var wg sync.WaitGroup
	var allowed atomic.Int64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requestsEach; i++ {
				if limiter.Allow() {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// Should never exceed the bucket's capacity in the first burst
	if allowed.Load() > 200 {
		t.Errorf("concurrent burst exceeded capacity: %d", allowed.Load())
	}
}
```

Run with:
```bash
go test -race ./...
```

---

## 4. AllowN Tests

Verify that `AllowN` atomically consumes N tokens:

```go
func TestAllowN(t *testing.T) {
	limiter := ratelimiter.NewTokenBucket(10, time.Second)

	if !limiter.AllowN(10) {
		t.Fatal("expected AllowN(10) to succeed with full bucket")
	}
	if limiter.AllowN(1) {
		t.Fatal("expected AllowN(1) to fail on empty bucket")
	}
}
```

---

## 5. Wait / Context Cancellation

Test that `Wait` respects context deadlines:

```go
func TestWaitRespectsContext(t *testing.T) {
	limiter := ratelimiter.NewTokenBucket(1, time.Second)
	limiter.Allow() // drain the bucket

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := limiter.Wait(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("Wait held too long: %v", elapsed)
	}
}
```

---

## 6. HTTP Middleware Integration Test

Test the middleware end-to-end with `net/http/httptest`:

```go
func TestRateLimitMiddleware(t *testing.T) {
	limiter := ratelimiter.NewTokenBucket(3, time.Second)
	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if i < 3 && resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, resp.StatusCode)
		}
		if i >= 3 && resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("request %d: expected 429, got %d", i+1, resp.StatusCode)
		}
	}
}
```

---

## 7. Running the Test Suite

```bash
# All unit tests
go test ./...

# With race detector
go test -race ./...

# Benchmarks
go test -bench=. -benchmem ./benchmarks/

# Verbose output for a single package
go test -v -run TestTokenBucket ./...
```

---

## See Also

- [algorithm-overview.md](algorithm-overview.md) — algorithm trade-offs
- [benchmarks/benchmark_test.go](../benchmarks/benchmark_test.go) — performance benchmarks
