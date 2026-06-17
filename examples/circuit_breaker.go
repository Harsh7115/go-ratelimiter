// examples/circuit_breaker.go
//
// Circuit breaker built on top of go-ratelimiter's token bucket.
//
// The circuit breaker has three states:
//
//   Closed   — normal operation; requests pass through.
//              A sliding-window error rate is tracked.
//   Open     — too many recent failures; all requests are rejected immediately.
//              After a configurable timeout the breaker moves to Half-Open.
//   Half-Open — one probe request is allowed through.  If it succeeds the
//              breaker closes; if it fails it opens again.
//
// This example combines a rate limiter (so the upstream isn't flooded even
// when closed) with circuit-breaker logic to stop hammering a failing service.
//
// Usage:
//   go run examples/circuit_breaker.go

package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// ---- Circuit-breaker states --------------------------------------------

type cbState int32

const (
	StateClosed   cbState = iota // normal
	StateOpen                     // failing fast
	StateHalfOpen                 // probing
)

func (s cbState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF-OPEN"
	}
	return "UNKNOWN"
}

// ---- CircuitBreaker ----------------------------------------------------

type CircuitBreaker struct {
	mu sync.Mutex

	state      cbState
	failures   int
	threshold  int           // failures before opening
	timeout    time.Duration // how long to stay open
	openedAt   time.Time

	// embedded rate limiter: cap requests even when closed
	limiter ratelimiter.Limiter

	// metrics
	totalRequests  atomic.Int64
	totalAllowed   atomic.Int64
	totalRejected  atomic.Int64
}

func NewCircuitBreaker(threshold int, openTimeout time.Duration, rps int) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		timeout:   openTimeout,
		limiter:   ratelimiter.NewTokenBucket(rps, time.Second),
	}
}

var ErrCircuitOpen = errors.New("circuit breaker is open")
var ErrRateLimited = errors.New("rate limit exceeded")

// Call executes fn if the breaker is closed (and under rate limit).
// It records the success or failure and transitions states accordingly.
func (cb *CircuitBreaker) Call(ctx context.Context, fn func() error) error {
	cb.totalRequests.Add(1)

	cb.mu.Lock()
	state := cb.state
	if state == StateOpen {
		if time.Since(cb.openedAt) > cb.timeout {
			cb.state = StateHalfOpen
			state = StateHalfOpen
		}
	}
	cb.mu.Unlock()

	if state == StateOpen {
		cb.totalRejected.Add(1)
		return ErrCircuitOpen
	}

	// Rate-limit check (only when closed/half-open)
	if !cb.limiter.Allow() {
		cb.totalRejected.Add(1)
		return ErrRateLimited
	}

	cb.totalAllowed.Add(1)
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failures++
		if cb.state == StateHalfOpen || cb.failures >= cb.threshold {
			cb.state = StateOpen
			cb.openedAt = time.Now()
			fmt.Printf("  [breaker] -> OPEN after %d failure(s)\n", cb.failures)
		}
		return err
	}

	// success
	if cb.state == StateHalfOpen {
		fmt.Printf("  [breaker] -> CLOSED (probe succeeded)\n")
		cb.state = StateClosed
		cb.failures = 0
	}
	return nil
}

func (cb *CircuitBreaker) State() cbState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ---- Simulated upstream ------------------------------------------------

// unreliableService succeeds 80% of the time normally but returns errors
// in bursts to trigger the breaker.
func unreliableService(failMode bool) error {
	if failMode || rand.Float64() < 0.8 {
		if failMode {
			return fmt.Errorf("upstream error")
		}
	}
	time.Sleep(5 * time.Millisecond) // simulate network latency
	return nil
}

// ---- main --------------------------------------------------------------

func main() {
	rand.Seed(time.Now().UnixNano())

	cb := NewCircuitBreaker(
		3,             // open after 3 consecutive failures
		500*time.Millisecond, // stay open for 500 ms
		50,            // max 50 req/s when closed
	)

	type result struct {
		attempt int
		state   cbState
		err     error
	}

	results := make([]result, 0, 30)
	failMode := false

	for i := 1; i <= 30; i++ {
		// Simulate a burst of failures on attempts 5–10
		failMode = i >= 5 && i <= 10

		ctx := context.Background()
		err := cb.Call(ctx, func() error {
			return unreliableService(failMode)
		})

		results = append(results, result{i, cb.State(), err})

		// Brief pause so the half-open timeout can fire
		time.Sleep(60 * time.Millisecond)
	}

	// Print summary
	fmt.Printf("\n%-8s %-12s %s\n", "ATTEMPT", "STATE", "RESULT")
	fmt.Printf("%-8s %-12s %s\n", "-------", "-----", "------")
	for _, r := range results {
		res := "ok"
		if r.err != nil {
			res = r.err.Error()
		}
		fmt.Printf("%-8d %-12s %s\n", r.attempt, r.state, res)
	}

	fmt.Printf("\nTotal: %d requests, %d allowed, %d rejected\n",
		cb.totalRequests.Load(),
		cb.totalAllowed.Load(),
		cb.totalRejected.Load(),
	)
}
