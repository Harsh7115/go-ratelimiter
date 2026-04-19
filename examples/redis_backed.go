// examples/redis_backed.go
//
// Demonstrates how to wrap go-ratelimiter's in-process limiters with a
// Redis-backed distributed layer so that rate limits are shared across
// multiple instances of your service.
//
// The pattern:
//   1. Use a local TokenBucket as a first-pass, in-memory gate (cheap).
//   2. On every request that passes the local gate, perform an atomic
//      Redis INCRBY + EXPIRE check to enforce the global cluster limit.
//
// This hybrid approach keeps the common case (local rejection) fast while
// still coordinating across pods for the global quota.
//
// Requirements:
//   go get github.com/Harsh7115/go-ratelimiter
//   go get github.com/redis/go-redis/v9
//
// Run:
//   # Start Redis locally
//   docker run -p 6379:6379 redis:7-alpine
//   go run examples/redis_backed.go

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
	"github.com/redis/go-redis/v9"
)

// ----------------------------------------------------------------------------
// RedisRateLimiter — distributed limiter backed by Redis counters
// ----------------------------------------------------------------------------

// RedisRateLimiter combines a local in-process gate with a Redis atomic counter
// so that the per-key request quota is enforced across the entire service fleet.
type RedisRateLimiter struct {
	rdb        *redis.Client
	localGate  *ratelimiter.TokenBucket // cheap first-pass filter
	globalRate int                      // max requests per window across all nodes
	window     time.Duration
	keyPrefix  string
}

// NewRedisRateLimiter creates a limiter that:
//   - allows at most localBurst requests per second per process (fast path)
//   - enforces globalRate requests per window across all processes (slow path)
func NewRedisRateLimiter(rdb *redis.Client, keyPrefix string, localBurst int, globalRate int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		rdb:        rdb,
		localGate:  ratelimiter.NewTokenBucket(localBurst, float64(localBurst)),
		globalRate: globalRate,
		window:     window,
		keyPrefix:  keyPrefix,
	}
}

// Allow returns true if the request identified by key is within quota.
func (r *RedisRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	// Fast path: reject immediately if the local burst bucket is exhausted.
	if !r.localGate.Allow() {
		return false, nil
	}

	// Slow path: atomically increment the Redis counter and check the global limit.
	redisKey := r.keyPrefix + ":" + key
	pipe := r.rdb.Pipeline()
	incr := pipe.Incr(ctx, redisKey)
	pipe.Expire(ctx, redisKey, r.window)
	if _, err := pipe.Exec(ctx); err != nil {
		// Redis unavailable — fail open to avoid cascading outages.
		log.Printf("redis rate-limit check failed (key=%s): %v — failing open", redisKey, err)
		return true, err
	}

	count := incr.Val()
	if count > int64(r.globalRate) {
		return false, nil
	}
	return true, nil
}

// Remaining returns the number of requests still available in the current window.
func (r *RedisRateLimiter) Remaining(ctx context.Context, key string) (int, error) {
	redisKey := r.keyPrefix + ":" + key
	val, err := r.rdb.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return r.globalRate, nil
	}
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(val)
	if err != nil {
		return 0, err
	}
	remaining := r.globalRate - count
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// ----------------------------------------------------------------------------
// HTTP server demo
// ----------------------------------------------------------------------------

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("cannot connect to Redis: %v", err)
	}
	fmt.Println("Connected to Redis")

	// Allow 5 requests/second locally, 20 requests/minute globally per client IP.
	limiter := NewRedisRateLimiter(rdb, "rl:demo", 5, 20, time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr // use a real IP extractor in production

		allowed, err := limiter.Allow(r.Context(), ip)
		if err != nil {
			log.Printf("limiter error: %v", err)
		}
		if !allowed {
			remaining, _ := limiter.Remaining(r.Context(), ip)
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("Retry-After", "60")
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}

		remaining, _ := limiter.Remaining(r.Context(), ip)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limiter.globalRate))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Window", limiter.window.String())
		fmt.Fprintf(w, "Hello! You have %d requests remaining in this window.\n", remaining)
	})

	addr := ":8080"
	fmt.Printf("Listening on %s\n", addr)
	fmt.Println("Try: for i in $(seq 1 25); do curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/api/hello; done")
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
