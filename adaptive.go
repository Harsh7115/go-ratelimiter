package ratelimiter

import (
	"sync"
	"time"
)

// AdaptiveLimiter implements an AIMD (Additive Increase, Multiplicative Decrease)
// rate limiter that automatically adjusts its token-bucket rate in response to
// downstream success/failure signals.
//
// Usage:
//
//	al := ratelimiter.NewAdaptiveLimiter(AdaptiveConfig{
//	    InitialRate: 50,
//	    MinRate:     5,
//	    MaxRate:     500,
//	    Capacity:    100,
//	    Increase:    2.0,   // tokens/sec added on success
//	    Decrease:    0.5,   // rate multiplied by this on failure
//	})
//
//	if al.Allow() {
//	    err := callDownstream()
//	    if err != nil {
//	        al.RecordFailure()
//	    } else {
//	        al.RecordSuccess()
//	    }
//	}
type AdaptiveLimiter struct {
	mu sync.Mutex

	// token bucket state
	tokens   float64
	capacity float64
	rate     float64 // current tokens/second
	lastTick time.Time

	// AIMD parameters
	minRate  float64
	maxRate  float64
	increase float64 // additive increase on success (tokens/sec)
	decrease float64 // multiplicative decrease factor on failure (0 < decrease < 1)

	// observability
	totalAllowed  int64
	totalThrottle int64
	successCount  int64
	failureCount  int64
}

// AdaptiveConfig holds construction parameters for AdaptiveLimiter.
type AdaptiveConfig struct {
	// InitialRate is the starting token-refill rate in tokens/second.
	InitialRate float64
	// MinRate is the floor for the dynamic rate. Must be > 0.
	MinRate float64
	// MaxRate is the ceiling for the dynamic rate.
	MaxRate float64
	// Capacity is the maximum token bucket size (controls burst).
	Capacity float64
	// Increase is the additive amount (tokens/sec) added to rate on each
	// RecordSuccess call. Defaults to 1.0 if zero.
	Increase float64
	// Decrease is the multiplicative factor applied to rate on each
	// RecordFailure call (e.g. 0.5 halves the rate). Defaults to 0.5 if zero.
	Decrease float64
}

// NewAdaptiveLimiter constructs an AdaptiveLimiter from the given config.
// Panics if MinRate <= 0 or MaxRate < MinRate.
func NewAdaptiveLimiter(cfg AdaptiveConfig) *AdaptiveLimiter {
	if cfg.MinRate <= 0 {
		panic("ratelimiter: AdaptiveConfig.MinRate must be > 0")
	}
	if cfg.MaxRate < cfg.MinRate {
		panic("ratelimiter: AdaptiveConfig.MaxRate must be >= MinRate")
	}
	if cfg.Increase == 0 {
		cfg.Increase = 1.0
	}
	if cfg.Decrease == 0 {
		cfg.Decrease = 0.5
	}
	rate := cfg.InitialRate
	if rate < cfg.MinRate {
		rate = cfg.MinRate
	}
	if rate > cfg.MaxRate {
		rate = cfg.MaxRate
	}
	return &AdaptiveLimiter{
		tokens:   cfg.Capacity,
		capacity: cfg.Capacity,
		rate:     rate,
		lastTick: time.Now(),
		minRate:  cfg.MinRate,
		maxRate:  cfg.MaxRate,
		increase: cfg.Increase,
		decrease: cfg.Decrease,
	}
}

// refill adds tokens proportional to time elapsed since last call.
// Must be called with al.mu held.
func (al *AdaptiveLimiter) refill() {
	now := time.Now()
	elapsed := now.Sub(al.lastTick).Seconds()
	al.lastTick = now
	al.tokens += elapsed * al.rate
	if al.tokens > al.capacity {
		al.tokens = al.capacity
	}
}

// Allow returns true and consumes one token if the limiter permits a request.
// Returns false (throttled) if no tokens are available.
func (al *AdaptiveLimiter) Allow() bool {
	return al.AllowN(1)
}

// AllowN returns true and consumes n tokens if sufficient tokens are available.
func (al *AdaptiveLimiter) AllowN(n int) bool {
	al.mu.Lock()
	defer al.mu.Unlock()

	al.refill()
	need := float64(n)
	if al.tokens >= need {
		al.tokens -= need
		al.totalAllowed++
		return true
	}
	al.totalThrottle++
	return false
}

// RecordSuccess signals that the most recent permitted request succeeded.
// The limiter responds by additively increasing its rate (up to MaxRate).
func (al *AdaptiveLimiter) RecordSuccess() {
	al.mu.Lock()
	defer al.mu.Unlock()

	al.successCount++
	al.rate += al.increase
	if al.rate > al.maxRate {
		al.rate = al.maxRate
	}
}

// RecordFailure signals that the most recent permitted request failed (e.g.
// downstream timeout or error). The limiter responds by multiplicatively
// decreasing its rate (down to MinRate).
func (al *AdaptiveLimiter) RecordFailure() {
	al.mu.Lock()
	defer al.mu.Unlock()

	al.failureCount++
	al.rate *= al.decrease
	if al.rate < al.minRate {
		al.rate = al.minRate
	}
}

// Rate returns the current token-refill rate in tokens/second.
func (al *AdaptiveLimiter) Rate() float64 {
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.rate
}

// Tokens returns the current number of available tokens.
func (al *AdaptiveLimiter) Tokens() float64 {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.refill()
	return al.tokens
}

// Reset restores the limiter to its initial state (full bucket, rate unchanged).
func (al *AdaptiveLimiter) Reset() {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.tokens = al.capacity
	al.lastTick = time.Now()
	al.totalAllowed = 0
	al.totalThrottle = 0
	al.successCount = 0
	al.failureCount = 0
}

// Stats returns a snapshot of the limiter's counters.
type AdaptiveStats struct {
	Rate          float64
	Tokens        float64
	TotalAllowed  int64
	TotalThrottle int64
	SuccessCount  int64
	FailureCount  int64
}

// Stats returns a point-in-time snapshot without mutating state.
func (al *AdaptiveLimiter) Stats() AdaptiveStats {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.refill()
	return AdaptiveStats{
		Rate:          al.rate,
		Tokens:        al.tokens,
		TotalAllowed:  al.totalAllowed,
		TotalThrottle: al.totalThrottle,
		SuccessCount:  al.successCount,
		FailureCount:  al.failureCount,
	}
}
