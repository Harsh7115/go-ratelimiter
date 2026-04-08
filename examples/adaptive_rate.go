// examples/adaptive_rate.go
//
// Adaptive rate-limiter demo: the allowed request rate is adjusted
// dynamically based on a simulated "system load" signal (0.0–1.0).
//
// Strategy
//   load < 0.4  → run at full rate  (highRate)
//   load < 0.7  → run at half rate  (highRate / 2)
//   load >= 0.7 → run at low rate   (lowRate)
//
// The AdaptiveLimiter wraps go-ratelimiter's TokenBucket and swaps the
// underlying bucket when the load tier changes.  A background goroutine
// polls the load source every loadPollInterval and updates the active
// bucket atomically via sync/atomic.
//
// Usage:
//   go run examples/adaptive_rate.go
//   go run examples/adaptive_rate.go -high 100 -low 10 -duration 20s

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// ── load simulation ───────────────────────────────────────────────────────────

// LoadSource returns a synthetic load value that oscillates between 0 and 1
// using a sine wave with added noise, simulating real server load patterns.
type LoadSource struct {
	mu    sync.Mutex
	phase float64
	rng   *rand.Rand
}

func NewLoadSource() *LoadSource {
	return &LoadSource{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func (ls *LoadSource) Load() float64 {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.phase += 0.05 // advance phase each call
	base := 0.5 + 0.45*math.Sin(ls.phase)
	noise := (ls.rng.Float64() - 0.5) * 0.1
	v := base + noise
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v
}

// ── adaptive limiter ──────────────────────────────────────────────────────────

type tier int

const (
	tierFull tier = iota
	tierHalf
	tierLow
)

func (t tier) String() string {
	switch t {
	case tierFull:
		return "FULL"
	case tierHalf:
		return "HALF"
	default:
		return "LOW"
	}
}

// AdaptiveLimiter dynamically switches between three token-bucket instances
// based on the current load tier.
type AdaptiveLimiter struct {
	// buckets indexed by tier constant
	buckets [3]*ratelimiter.TokenBucket

	// current active bucket pointer (atomic)
	active unsafe.Pointer // *ratelimiter.TokenBucket

	currentTier tier
	mu          sync.Mutex
}

func NewAdaptiveLimiter(highRate, lowRate float64) *AdaptiveLimiter {
	al := &AdaptiveLimiter{}
	al.buckets[tierFull] = ratelimiter.NewTokenBucket(highRate, int(highRate))
	al.buckets[tierHalf] = ratelimiter.NewTokenBucket(highRate/2, int(highRate/2))
	al.buckets[tierLow] = ratelimiter.NewTokenBucket(lowRate, int(lowRate))
	atomic.StorePointer(&al.active, unsafe.Pointer(al.buckets[tierFull]))
	al.currentTier = tierFull
	return al
}

func (al *AdaptiveLimiter) Tier() tier {
	al.mu.Lock()
	defer al.mu.Unlock()
	return al.currentTier
}

// Update evaluates load and, if the tier changes, swaps the active bucket.
// Returns true if a tier change occurred.
func (al *AdaptiveLimiter) Update(load float64) bool {
	var newTier tier
	switch {
	case load < 0.4:
		newTier = tierFull
	case load < 0.7:
		newTier = tierHalf
	default:
		newTier = tierLow
	}

	al.mu.Lock()
	defer al.mu.Unlock()
	if newTier == al.currentTier {
		return false
	}
	al.currentTier = newTier
	atomic.StorePointer(&al.active, unsafe.Pointer(al.buckets[newTier]))
	return true
}

// Allow returns true if the current bucket permits a request.
func (al *AdaptiveLimiter) Allow() bool {
	b := (*ratelimiter.TokenBucket)(atomic.LoadPointer(&al.active))
	return b.Allow()
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	highRate := flag.Float64("high", 50.0, "full-tier rate (req/s)")
	lowRate := flag.Float64("low", 5.0, "low-tier rate (req/s)")
	duration := flag.Duration("duration", 15*time.Second, "how long to run")
	loadPoll := flag.Duration("poll", 200*time.Millisecond, "load poll interval")
	reqInterval := flag.Duration("req", 10*time.Millisecond, "simulated request interval")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	src := NewLoadSource()
	limiter := NewAdaptiveLimiter(*highRate, *lowRate)

	var (
		allowed  int64
		rejected int64
		changes  int64
	)

	// Background load monitor — updates the limiter tier.
	go func() {
		ticker := time.NewTicker(*loadPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				load := src.Load()
				if limiter.Update(load) {
					atomic.AddInt64(&changes, 1)
					log.Printf("tier → %s  (load=%.2f)", limiter.Tier(), load)
				}
			}
		}
	}()

	// Simulated request loop.
	ticker := time.NewTicker(*reqInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
			if limiter.Allow() {
				atomic.AddInt64(&allowed, 1)
			} else {
				atomic.AddInt64(&rejected, 1)
			}
		}
	}

done:
	total := allowed + rejected
	pctAllowed := 0.0
	if total > 0 {
		pctAllowed = float64(allowed) / float64(total) * 100
	}
	fmt.Printf("\n── Adaptive Rate Limiter Results ────────────────────\n")
	fmt.Printf("  Duration:    %v\n", *duration)
	fmt.Printf("  High rate:   %.0f req/s   Low rate: %.0f req/s\n", *highRate, *lowRate)
	fmt.Printf("  Tier changes: %d\n", changes)
	fmt.Printf("  Allowed:     %d / %d  (%.1f%%)\n", allowed, total, pctAllowed)
	fmt.Printf("  Rejected:    %d\n", rejected)
}
