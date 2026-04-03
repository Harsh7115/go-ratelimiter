# Middleware Integration Guide

This guide shows how to drop **go-ratelimiter** into common Go HTTP stacks as
middleware so every incoming request is automatically rate-limited before it
reaches your handler.

---

## Table of Contents

1. [net/http — standard library](#1-nethttp--standard-library)
2. [Per-IP rate limiting with net/http](#2-per-ip-rate-limiting-with-nethttp)
3. [Gin framework](#3-gin-framework)
4. [Echo framework](#4-echo-framework)
5. [Returning correct HTTP headers](#5-returning-correct-http-headers)
6. [Choosing the right algorithm for your use case](#6-choosing-the-right-algorithm-for-your-use-case)
7. [Testing middleware in unit tests](#7-testing-middleware-in-unit-tests)

---

## 1. net/http — standard library

The simplest integration wraps any `http.Handler` with a single global limiter.

```go
package main

import (
	"net/http"
	"time"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// RateLimitMiddleware wraps h and enforces a global token-bucket limit.
func RateLimitMiddleware(h http.Handler, capacity int, rate float64) http.Handler {
	limiter := ratelimiter.NewTokenBucket(capacity, rate)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Allow bursts up to 50 requests; refill at 10 req/s
	handler := RateLimitMiddleware(mux, 50, 10)

	http.ListenAndServe(":8080", handler)
}
```

---

## 2. Per-IP rate limiting with net/http

A single global limiter treats all clients equally.  For public APIs you
usually want **per-client** limits so one abusive IP does not crowd out others.

```go
package main

import (
	"net"
	"net/http"
	"sync"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// IPRateLimiter maintains one TokenBucket per remote IP.
type IPRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ratelimiter.TokenBucket
	capacity int
	rate     float64
}

func NewIPRateLimiter(capacity int, rate float64) *IPRateLimiter {
	return &IPRateLimiter{
		limiters: make(map[string]*ratelimiter.TokenBucket),
		capacity: capacity,
		rate:     rate,
	}
}

func (irl *IPRateLimiter) get(ip string) *ratelimiter.TokenBucket {
	irl.mu.Lock()
	defer irl.mu.Unlock()
	if tb, ok := irl.limiters[ip]; ok {
		return tb
	}
	tb := ratelimiter.NewTokenBucket(irl.capacity, irl.rate)
	irl.limiters[ip] = tb
	return tb
}

// Middleware returns an http.Handler that applies per-IP limiting.
func (irl *IPRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !irl.get(ip).Allow() {
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	ipLimiter := NewIPRateLimiter(20, 5) // 20-burst, 5 req/s per IP

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello!"))
	})

	http.ListenAndServe(":8080", ipLimiter.Middleware(mux))
}
```

> **Memory note:** The limiter map grows unboundedly.  In production, evict
> entries that have been idle for longer than your window duration (e.g. wrap
> with a TTL cache or use a time-based eviction goroutine).

---

## 3. Gin framework

```go
package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// SlidingWindowMiddleware limits the entire API to 200 req/min.
func SlidingWindowMiddleware(limit int) gin.HandlerFunc {
	sw := ratelimiter.NewSlidingWindow(limit, 60*1e9) // time.Minute as nanoseconds

	return func(c *gin.Context) {
		if !sw.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			return
		}
		c.Next()
	}
}

func main() {
	r := gin.Default()

	// Apply globally
	r.Use(SlidingWindowMiddleware(200))

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	r.Run(":8080")
}
```

For **per-route** limits, apply the middleware only to specific groups:

```go
// Strict limit on auth endpoints
auth := r.Group("/auth")
auth.Use(SlidingWindowMiddleware(10)) // 10 req/min on /auth/*
{
	auth.POST("/login", loginHandler)
	auth.POST("/register", registerHandler)
}
```

---

## 4. Echo framework

```go
package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// LeakyBucketMiddleware enforces a constant output rate via leaky bucket.
func LeakyBucketMiddleware(capacity int, rate float64) echo.MiddlewareFunc {
	lb := ratelimiter.NewLeakyBucket(capacity, rate)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !lb.Allow() {
				return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
			}
			return next(c)
		}
	}
}

func main() {
	e := echo.New()

	e.Use(LeakyBucketMiddleware(100, 20)) // queue 100, drain at 20 req/s

	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello!")
	})

	e.Start(":8080")
}
```

---

## 5. Returning correct HTTP headers

RFC 6585 and common API conventions expect rate-limit information in response
headers.  Add these to every response so clients can back off intelligently:

```go
func tokenBucketMiddleware(next http.Handler, tb *ratelimiter.TokenBucket, capacity int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remaining := int(tb.Tokens())

		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", capacity))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		if !tb.Allow() {
			w.Header().Set("Retry-After", "1") // seconds until next token
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

---

## 6. Choosing the right algorithm for your use case

| Scenario                                  | Recommended algorithm | Why                                      |
|-------------------------------------------|-----------------------|------------------------------------------|
| Public REST API with occasional bursts    | Token Bucket          | Allows legitimate burst traffic          |
| Login / auth endpoints (no burst)         | Sliding Window        | Precise per-minute counts                |
| Downstream service with fixed throughput  | Leaky Bucket          | Smooths bursty callers to constant rate  |
| Websocket message rate                    | Token Bucket          | Low overhead, O(1) per message           |
| Expensive DB queries per user per hour    | Sliding Window        | Exact window semantics matter here       |

---

## 7. Testing middleware in unit tests

Use `net/http/httptest` to exercise the middleware without a real server:

```go
func TestRateLimitMiddleware_Blocks(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 2-token capacity, 1 token/s — third request must be blocked
	handler := RateLimitMiddleware(inner, 2, 1.0)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}
```
