// examples/http_middleware.go demonstrates all three go-ratelimiter algorithms
// wired up as composable net/http middleware. Run the server, then use the
// test script below to observe HIT, 429, and per-algorithm behaviour.
//
// Usage:
//
//	go run ./examples/http_middleware.go
//
// Quick test (requires curl and a POSIX shell):
//
//	# Token bucket — bursts allowed
//	for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/token; done
//
//	# Sliding window — strict per-minute limit
//	for i in $(seq 1 6); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/window; done
//
//	# Leaky bucket — constant drain rate
//	for i in $(seq 1 8); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/leaky; done
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	rl "github.com/Harsh7115/go-ratelimiter"
)

// ---------------------------------------------------------------------------
// Middleware constructors
// ---------------------------------------------------------------------------

// tokenBucketMiddleware admits requests while tokens remain; refills at rate/sec.
func tokenBucketMiddleware(capacity int, rate float64, next http.Handler) http.Handler {
	limiter := rl.NewTokenBucket(capacity, rate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			rateLimitedResponse(w, "token_bucket", limiter.Tokens())
			return
		}
		w.Header().Set("X-RateLimit-Algorithm", "token_bucket")
		w.Header().Set("X-RateLimit-Remaining", jsonFloat(limiter.Tokens()))
		next.ServeHTTP(w, r)
	})
}

// slidingWindowMiddleware admits at most limit requests per window duration.
func slidingWindowMiddleware(limit int, window time.Duration, next http.Handler) http.Handler {
	limiter := rl.NewSlidingWindow(limit, window)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			rateLimitedResponse(w, "sliding_window", float64(limiter.Count()))
			return
		}
		w.Header().Set("X-RateLimit-Algorithm", "sliding_window")
		w.Header().Set("X-RateLimit-Count", jsonInt(limiter.Count()))
		next.ServeHTTP(w, r)
	})
}

// leakyBucketMiddleware queues requests and drains them at a constant rate.
func leakyBucketMiddleware(capacity int, rate float64, next http.Handler) http.Handler {
	limiter := rl.NewLeakyBucket(capacity, rate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			rateLimitedResponse(w, "leaky_bucket", float64(limiter.QueueDepth()))
			return
		}
		w.Header().Set("X-RateLimit-Algorithm", "leaky_bucket")
		w.Header().Set("X-RateLimit-Queue-Depth", jsonInt(limiter.QueueDepth()))
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type apiResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	Algorithm string `json:"algorithm"`
}

func makeHandler(algo string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{
			Message:   "request accepted",
			Timestamp: time.Now().Format(time.RFC3339Nano),
			Algorithm: algo,
		})
	}
}

type rateLimitError struct {
	Error     string  `json:"error"`
	Algorithm string  `json:"algorithm"`
	Counter   float64 `json:"counter"`
}

func rateLimitedResponse(w http.ResponseWriter, algo string, counter float64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(rateLimitError{
		Error:     "rate limit exceeded",
		Algorithm: algo,
		Counter:   counter,
	})
	log.Printf("[%s] 429 Too Many Requests  counter=%.1f", algo, counter)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func jsonInt(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	const addr = ":8080"

	mux := http.NewServeMux()

	// /api/token  — token bucket: 10 capacity, 2 tokens/sec
	mux.Handle("/api/token",
		tokenBucketMiddleware(10, 2, makeHandler("token_bucket")),
	)

	// /api/window — sliding window: 5 requests per 30 seconds
	mux.Handle("/api/window",
		slidingWindowMiddleware(5, 30*time.Second, makeHandler("sliding_window")),
	)

	// /api/leaky  — leaky bucket: 6 capacity, 3 requests/sec drain
	mux.Handle("/api/leaky",
		leakyBucketMiddleware(6, 3, makeHandler("leaky_bucket")),
	)

	// /api/status — no rate limiting; shows all limiter states at once
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token_bucket":   "capacity=10  rate=2/s",
			"sliding_window": "limit=5  window=30s",
			"leaky_bucket":   "capacity=6  drain=3/s",
		})
	})

	log.Printf("Listening on %s", addr)
	log.Println("Endpoints:")
	log.Println("  GET /api/token   — token bucket  (capacity=10, rate=2/s)")
	log.Println("  GET /api/window  — sliding window (limit=5, window=30s)")
	log.Println("  GET /api/leaky   — leaky bucket   (capacity=6, drain=3/s)")
	log.Println("  GET /api/status  — limiter configs (no rate limit)")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
