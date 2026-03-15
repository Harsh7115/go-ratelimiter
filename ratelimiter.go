// Package ratelimiter provides three classic rate-limiting algorithms:
//
//   - TokenBucket  – allows bursts up to capacity; refills at a constant rate.
//   - SlidingWindow – counts requests in a rolling time window; smooth and fair.
//   - LeakyBucket  – drains at a fixed rate regardless of arrival pattern.
//
// All implementations are safe for concurrent use by multiple goroutines and
// have zero external dependencies.
package ratelimiter

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Limiter — common interface
// ---------------------------------------------------------------------------

// Limiter is the interface implemented by every rate limiter in this package.
type Limiter interface {
	// Allow reports whether the caller is allowed to proceed right now.
	// It consumes one token / slot from the limiter if allowed.
	Allow() bool

	// AllowN reports whether n tokens / slots are available right now and
	// consumes them if so.  n must be positive.
	AllowN(n int) bool

	// Reset resets the limiter to its initial state.
	Reset()
}

// ---------------------------------------------------------------------------
// TokenBucket
// ---------------------------------------------------------------------------

// TokenBucket implements the token-bucket algorithm.
//
// Tokens accumulate at a fixed rate (rate tokens/second) up to a maximum of
// capacity.  Each Allow call consumes one token; Allow returns false when the
// bucket is empty.  Burst traffic up to capacity is naturally handled.
type TokenBucket struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	rate     float64 // tokens per nanosecond
	lastTick time.Time
}

// NewTokenBucket creates a TokenBucket that allows up to capacity tokens in a
// burst and refills at rate tokens per second.
func NewTokenBucket(capacity int, rate float64) *TokenBucket {
	if capacity <= 0 {
		panic("ratelimiter: TokenBucket capacity must be positive")
	}
	if rate <= 0 {
		panic("ratelimiter: TokenBucket rate must be positive")
	}
	return &TokenBucket{
		capacity: float64(capacity),
		tokens:   float64(capacity),
		rate:     rate / 1e9, // convert to tokens/ns
		lastTick: time.Now(),
	}
}

// refill adds tokens earned since lastTick.  Must be called with mu held.
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := float64(now.Sub(tb.lastTick))
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}
	tb.lastTick = now
}

// Allow implements Limiter.
func (tb *TokenBucket) Allow() bool { return tb.AllowN(1) }

// AllowN implements Limiter.
func (tb *TokenBucket) AllowN(n int) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	if tb.tokens >= float64(n) {
		tb.tokens -= float64(n)
		return true
	}
	return false
}

// Tokens returns the current number of available tokens (for observability).
func (tb *TokenBucket) Tokens() float64 {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	return tb.tokens
}

// Reset implements Limiter.
func (tb *TokenBucket) Reset() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.tokens = tb.capacity
	tb.lastTick = time.Now()
}

// ---------------------------------------------------------------------------
// SlidingWindow
// ---------------------------------------------------------------------------

// SlidingWindow implements a sliding-window counter rate limiter.
//
// It tracks individual request timestamps within the window and allows at most
// limit requests per window duration.  Unlike a fixed-window counter it avoids
// the boundary-burst problem: the limit is enforced over any rolling window of
// the given duration.
type SlidingWindow struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	timestamps []time.Time
}

// NewSlidingWindow creates a SlidingWindow that allows at most limit requests
// within any rolling window of the given duration.
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	if limit <= 0 {
		panic("ratelimiter: SlidingWindow limit must be positive")
	}
	if window <= 0 {
		panic("ratelimiter: SlidingWindow window must be positive")
	}
	return &SlidingWindow{
		limit:      limit,
		window:     window,
		timestamps: make([]time.Time, 0, limit),
	}
}

// evict removes timestamps older than window from the front of the slice.
// Must be called with mu held.
func (sw *SlidingWindow) evict(now time.Time) {
	cutoff := now.Add(-sw.window)
	i := 0
	for i < len(sw.timestamps) && sw.timestamps[i].Before(cutoff) {
		i++
	}
	sw.timestamps = sw.timestamps[i:]
}

// Allow implements Limiter.
func (sw *SlidingWindow) Allow() bool { return sw.AllowN(1) }

// AllowN implements Limiter.
func (sw *SlidingWindow) AllowN(n int) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	now := time.Now()
	sw.evict(now)
	if len(sw.timestamps)+n > sw.limit {
		return false
	}
	for i := 0; i < n; i++ {
		sw.timestamps = append(sw.timestamps, now)
	}
	return true
}

// Count returns the number of requests recorded in the current window.
func (sw *SlidingWindow) Count() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.evict(time.Now())
	return len(sw.timestamps)
}

// Reset implements Limiter.
func (sw *SlidingWindow) Reset() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.timestamps = sw.timestamps[:0]
}

// ---------------------------------------------------------------------------
// LeakyBucket
// ---------------------------------------------------------------------------

// LeakyBucket implements the leaky-bucket algorithm.
//
// Requests are added to a virtual queue (bucket) of fixed capacity.  The
// bucket drains at a constant rate; Allow returns true only when there is
// space in the bucket.  This enforces a smooth, constant output rate and
// drops bursts that exceed capacity.
type LeakyBucket struct {
	mu       sync.Mutex
	capacity int
	queue    int           // number of requests currently in the bucket
	rate     float64       // drain rate in requests per nanosecond
	lastDrain time.Time
}

// NewLeakyBucket creates a LeakyBucket with the given capacity (maximum burst)
// that drains at rate requests per second.
func NewLeakyBucket(capacity int, rate float64) *LeakyBucket {
	if capacity <= 0 {
		panic("ratelimiter: LeakyBucket capacity must be positive")
	}
	if rate <= 0 {
		panic("ratelimiter: LeakyBucket rate must be positive")
	}
	return &LeakyBucket{
		capacity:  capacity,
		rate:      rate / 1e9,
		lastDrain: time.Now(),
	}
}

// drain removes requests that have leaked out since lastDrain.
// Must be called with mu held.
func (lb *LeakyBucket) drain() {
	now := time.Now()
	elapsed := float64(now.Sub(lb.lastDrain))
	drained := int(elapsed * lb.rate)
	if drained > 0 {
		lb.queue -= drained
		if lb.queue < 0 {
			lb.queue = 0
		}
		lb.lastDrain = now
	}
}

// Allow implements Limiter.
func (lb *LeakyBucket) Allow() bool { return lb.AllowN(1) }

// AllowN implements Limiter.
func (lb *LeakyBucket) AllowN(n int) bool {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.drain()
	if lb.queue+n > lb.capacity {
		return false
	}
	lb.queue += n
	return true
}

// QueueDepth returns the current number of requests waiting in the bucket.
func (lb *LeakyBucket) QueueDepth() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.drain()
	return lb.queue
}

// Reset implements Limiter.
func (lb *LeakyBucket) Reset() {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.queue = 0
	lb.lastDrain = time.Now()
}
