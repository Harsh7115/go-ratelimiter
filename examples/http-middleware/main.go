// Package main demonstrates using go-ratelimiter as HTTP middleware.
// It shows how to wrap a standard http.Handler with token-bucket rate limiting,
// returning 429 Too Many Requests when the limit is exceeded.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// RateLimitMiddleware wraps an http.Handler and applies a per-IP token-bucket limiter.
type RateLimitMiddleware struct {
	next     http.Handler
	limiters map[string]*ratelimiter.TokenBucket
	rate     float64 // tokens per second
	capacity int     // burst size
}

// NewRateLimitMiddleware creates middleware with the given rate (req/s) and burst capacity.
func NewRateLimitMiddleware(next http.Handler, rate float64, capacity int) *RateLimitMiddleware {
	return &RateLimitMiddleware{
		next:     next,
		limiters: make(map[string]*ratelimiter.TokenBucket),
		rate:     rate,
		capacity: capacity,
	}
}

// limiterFor returns (or lazily creates) the token bucket for the given IP.
func (m *RateLimitMiddleware) limiterFor(ip string) *ratelimiter.TokenBucket {
	if l, ok := m.limiters[ip]; ok {
		return l
	}
	l := ratelimiter.NewTokenBucket(m.rate, m.capacity)
	m.limiters[ip] = l
	return l
}

// ServeHTTP implements http.Handler.
func (m *RateLimitMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr // In production, parse X-Forwarded-For instead.
	limiter := m.limiterFor(ip)

	if !limiter.Allow() {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "rate limit exceeded",
			"message": fmt.Sprintf("max %.0f req/s allowed", m.rate),
		})
		return
	}

	m.next.ServeHTTP(w, r)
}

// --- Handlers ---

func helloHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":   "Hello, World!",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", helloHandler)
	mux.HandleFunc("/health", healthHandler)

	// Wrap the entire mux: allow 10 req/s with a burst of 20.
	limited := NewRateLimitMiddleware(mux, 10, 20)

	addr := ":8080"
	log.Printf("Server listening on %s (10 req/s per IP, burst 20)", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      limited,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
