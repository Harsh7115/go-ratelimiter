// per_client.go -- per-IP / per-client rate limiting with automatic eviction
//
// A single global limiter is often too coarse: a well-behaved client should not
// be throttled because a misbehaving one is hammering your API. This example
// shows how to maintain a per-client map of limiters and evict idle entries so
// the map does not grow unbounded.
//
// Run:
//   go run per_client.go
//   # Then in another terminal:
//   for i in $(seq 1 20); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api; done
//   # Send traffic from a "different" IP via the X-Forwarded-For header:
//   curl -H "X-Forwarded-For: 10.0.0.2" http://localhost:8080/api

package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// ---------------------------------------------------------------
// ClientLimiter: a registry of per-client token-bucket limiters
// ---------------------------------------------------------------

// entry wraps a Limiter with the last-seen timestamp for TTL eviction.
type entry struct {
	limiter  *ratelimiter.TokenBucket
	lastSeen time.Time
}

// ClientLimiter holds one TokenBucket per client key (IP address by default).
type ClientLimiter struct {
	mu       sync.Mutex
	clients  map[string]*entry
	capacity int
	rate     float64       // tokens per second
	ttl      time.Duration // evict entries idle for this long
}

// NewClientLimiter creates a registry.
//   - capacity: maximum burst size per client (token bucket capacity)
//   - rate:     refill rate in tokens/second
//   - ttl:      how long an idle entry is kept before eviction
func NewClientLimiter(capacity int, rate float64, ttl time.Duration) *ClientLimiter {
	cl := &ClientLimiter{
		clients:  make(map[string]*entry),
		capacity: capacity,
		rate:     rate,
		ttl:      ttl,
	}
	go cl.evictLoop()
	return cl
}

// Allow returns true if the client identified by key is within its rate limit.
func (cl *ClientLimiter) Allow(key string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	e, ok := cl.clients[key]
	if !ok {
		e = &entry{
			limiter: ratelimiter.NewTokenBucket(cl.capacity, cl.rate),
		}
		cl.clients[key] = e
	}
	e.lastSeen = time.Now()
	return e.limiter.Allow()
}

// evictLoop runs in the background and removes entries that have been idle
// for longer than ttl, preventing unbounded memory growth.
func (cl *ClientLimiter) evictLoop() {
	ticker := time.NewTicker(cl.ttl / 2)
	defer ticker.Stop()
	for range ticker.C {
		cl.mu.Lock()
		cutoff := time.Now().Add(-cl.ttl)
		for key, e := range cl.clients {
			if e.lastSeen.Before(cutoff) {
				delete(cl.clients, key)
			}
		}
		cl.mu.Unlock()
	}
}

// Len returns the number of tracked clients (useful for metrics).
func (cl *ClientLimiter) Len() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return len(cl.clients)
}

// ---------------------------------------------------------------
// Helper: extract the real client IP from a request
// ---------------------------------------------------------------

// clientIP returns the originating IP, honouring X-Forwarded-For if present
// (e.g. when running behind a reverse proxy). In production you should only
// trust this header from known trusted proxies.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be a comma-separated list; take the first entry.
		if ip, _, err := net.SplitHostPort(xff); err == nil {
			return ip
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------
// HTTP middleware
// ---------------------------------------------------------------

// PerClientMiddleware wraps an http.Handler and applies per-client rate
// limiting. Clients that exceed their limit receive 429 Too Many Requests.
func PerClientMiddleware(limiter *ClientLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !limiter.Allow(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			log.Printf("RATE_LIMIT ip=%s path=%s tracked_clients=%d", ip, r.URL.Path, limiter.Len())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------
// Demo server
// ---------------------------------------------------------------

func main() {
	// Each client IP gets: burst of 5 requests, refilling at 2 req/s.
	// Idle clients are evicted after 5 minutes.
	limiter := NewClientLimiter(5, 2.0, 5*time.Minute)

	mux := http.NewServeMux()

	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK — served to %s\n", clientIP(r))
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tracked clients: %d\n", limiter.Len())
	})

	handler := PerClientMiddleware(limiter, mux)

	addr := ":8080"
	log.Printf("listening on %s (burst=5, rate=2 req/s per IP, evict TTL=5m)", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
