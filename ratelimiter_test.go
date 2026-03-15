package ratelimiter_test

import (
	"sync"
	"testing"
	"time"

	rl "github.com/Harsh7115/go-ratelimiter"
)

// ── TokenBucket ──────────────────────────────────────────────────────────────

func TestTokenBucket_Allow(t *testing.T) {
	tb := rl.NewTokenBucket(3, 100) // cap=3, 100 tok/s
	for i := 0; i < 3; i++ {
		if !tb.Allow() {
			t.Fatalf("Allow() returned false on call %d (bucket should still have tokens)", i+1)
		}
	}
	if tb.Allow() {
		t.Fatal("Allow() returned true when bucket should be empty")
	}
}

func TestTokenBucket_AllowN(t *testing.T) {
	tb := rl.NewTokenBucket(5, 100)
	if !tb.AllowN(3) {
		t.Fatal("AllowN(3) should succeed with capacity 5")
	}
	if tb.AllowN(3) {
		t.Fatal("AllowN(3) should fail: only 2 tokens remain")
	}
	if !tb.AllowN(2) {
		t.Fatal("AllowN(2) should succeed with 2 tokens remaining")
	}
}

func TestTokenBucket_Refill(t *testing.T) {
	tb := rl.NewTokenBucket(1, 1000) // 1000 tok/s = 1 tok/ms
	if !tb.Allow() {
		t.Fatal("first Allow should succeed")
	}
	if tb.Allow() {
		t.Fatal("second Allow should fail immediately")
	}
	time.Sleep(5 * time.Millisecond)
	if !tb.Allow() {
		t.Fatal("Allow should succeed after refill delay")
	}
}

func TestTokenBucket_Reset(t *testing.T) {
	tb := rl.NewTokenBucket(2, 100)
	tb.Allow()
	tb.Allow()
	tb.Reset()
	if !tb.Allow() {
		t.Fatal("Allow should succeed after Reset")
	}
}

func TestTokenBucket_Panic_BadCap(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for capacity <= 0")
		}
	}()
	rl.NewTokenBucket(0, 10)
}

func TestTokenBucket_Concurrent(t *testing.T) {
	tb := rl.NewTokenBucket(100, 1) // rate=1/s: no meaningful refill during concurrent test
	var wg sync.WaitGroup
	allowed := make(chan bool, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- tb.Allow()
		}()
	}
	wg.Wait()
	close(allowed)
	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}
	if count > 100 {
		t.Fatalf("allowed %d requests but capacity is 100", count)
	}
}

// ── SlidingWindow ─────────────────────────────────────────────────────────────

func TestSlidingWindow_Allow(t *testing.T) {
	sw := rl.NewSlidingWindow(3, time.Second)
	for i := 0; i < 3; i++ {
		if !sw.Allow() {
			t.Fatalf("Allow() returned false on call %d", i+1)
		}
	}
	if sw.Allow() {
		t.Fatal("Allow() should return false when limit is reached")
	}
}

func TestSlidingWindow_AllowN(t *testing.T) {
	sw := rl.NewSlidingWindow(5, time.Second)
	if !sw.AllowN(5) {
		t.Fatal("AllowN(5) should succeed")
	}
	if sw.AllowN(1) {
		t.Fatal("AllowN(1) should fail after 5 allowed")
	}
}

func TestSlidingWindow_WindowSlides(t *testing.T) {
	sw := rl.NewSlidingWindow(2, 50*time.Millisecond)
	if !sw.AllowN(2) {
		t.Fatal("first batch should be allowed")
	}
	if sw.Allow() {
		t.Fatal("should be blocked immediately")
	}
	time.Sleep(60 * time.Millisecond) // window expires
	if !sw.Allow() {
		t.Fatal("should be allowed after window slides")
	}
}

func TestSlidingWindow_Count(t *testing.T) 
	sw := rl.NewSlidingWindow(10, time.Second)
	sw.AllowN(4)
	if c := sw.Count(); c != 4 {
		t.Fatalf("Count() = %d, want 4", c)
	}
}

func TestSlidingWindow_Reset(t *testing.T) {
	sw := rl.NewSlidingWindow(2, time.Second)
	sw.AllowN(2)
	sw.Reset()
	if !sw.Allow() {
		t.Fatal("Allow should succeed after Reset")
	}
}

func TestSlidingWindow_Concurrent(t *testing.T) {
	sw := rl.NewSlidingWindow(50, time.Second)
	var wg sync.WaitGroup
	allowed := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- sw.Allow()
		}()
	}
	wg.Wait()
	close(allowed)
	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}
	if count > 50 {
		t.Fatalf("allowed %d requests but limit is 50", count)
	}
}

// ── LeakyBucket ───────────────────────────────────────────────────────────────

func TestLeakyBucket_Allow(t *testing.T) {
	lb := rl.NewLeakyBucket(3, 100)
	for i := 0; i < 3; i++ {
		if !lb.Allow() {
			t.Fatalf("Allow() returned false on call %d", i+1)
		}
	}
	if lb.Allow() {
		t.Fatal("Allow() should return false when bucket is full")
	}
}

func TestLeakyBucket_Drains(t *testing.T) {
	lb := rl.NewLeakyBucket(2, 1000) // drains at 1000/s = 1/ms
	lb.AllowN(2)
	if lb.Allow() {
		t.Fatal("should be blocked when full")
	}
	time.Sleep(5 * time.Millisecond) // enough to drain at least 2
	if !lb.Allow() {
		t.Fatal("should be allowed after draining")
	}
}

func TestLeakyBucket_AllowN(t *testing.T) {
	lb := rl.NewLeakyBucket(5, 100)
	if !lb.AllowN(5) {
		t.Fatal("AllowN(5) should fill the bucket")
	}
	if lb.AllowN(1) {
		t.Fatal("AllowN(1) should fail when bucket is full")
	}
}

func TestLeakyBucket_Reset(t *testing.T) {
	lb := rl.NewLeakyBucket(2, 100)
	lb.AllowN(2)
	lb.Reset()
	if !lb.Allow() {
		t.Fatal("Allow should succeed after Reset")
	}
}

func TestLeakyBucket_QueueDepth(t *testing.T) {
	lb := rl.NewLeakyBucket(10, 100)
	lb.AllowN(4)
	if d := lb.QueueDepth(); d != 4 {
		t.Fatalf("QueueDepth() = %d, want 4", d)
	}
}

func TestLeakyBucket_Concurrent(t *testing.T) {
	lb := rl.NewLeakyBucket(50, 1) // rate=1/s: no meaningful drain during concurrent test
	var wg sync.WaitGroup
	allowed := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- lb.Allow()
		}()
	}
	wg.Wait()
	close(allowed)
	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}
	if count > 50 {
		t.Fatalf("allowed %d requests but capacity is 50", count)
	}
}

// ── Interface compliance ───────────────────────────────────────────────────────

func TestLimiterInterface(t *testing.T) {
	limiters := []rl.Limiter{
		rl.NewTokenBucket(10, 100),
		rl.NewSlidingWindow(10, time.Second),
		rl.NewLeakyBucket(10, 100),
	}
	for _, l := range limiters {
		if !l.Allow() {
			t.Errorf("%T.Allow() should return true on fresh limiter", l)
		}
		l.Reset()
	}
}
