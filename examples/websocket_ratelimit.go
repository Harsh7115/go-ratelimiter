// Package main demonstrates per-connection WebSocket message rate limiting
// using go-ratelimiter. Each WebSocket connection gets its own token bucket;
// when a client exceeds its budget the server sends a "slow down" frame and
// optionally closes the connection if the client persists.
//
// Run with:  go run examples/websocket_ratelimit.go
//   then:    websocat ws://localhost:8080/echo
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	rl "github.com/Harsh7115/go-ratelimiter"
)

// Per-connection budget: 20 messages per second with a burst of 30.
const (
	msgsPerSecond = 20
	burst         = 30
	graceFrames   = 3 // how many warnings before we drop the connection
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// connState tracks per-connection limiter and warning count.
type connState struct {
	limiter   *rl.TokenBucket
	warnings  int
	closeOnce sync.Once
}

func newConnState() *connState {
	return &connState{
		limiter: rl.NewTokenBucket(msgsPerSecond, burst),
	}
}

// echo accepts and reflects WebSocket messages, rate-limiting per connection.
func echo(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer c.Close()

	state := newConnState()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	for {
		mt, msg, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			return
		}

		if !state.limiter.Allow() {
			state.warnings++
			warn := fmt.Sprintf("rate-limit exceeded; warning %d/%d", state.warnings, graceFrames)
			_ = c.WriteMessage(websocket.TextMessage, []byte(warn))
			if state.warnings >= graceFrames {
				state.closeOnce.Do(func() {
					_ = c.WriteControl(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "too many messages"),
						time.Now().Add(time.Second),
					)
					cancel()
				})
				return
			}
			continue
		}

		// Echo the message back.
		if err := c.WriteMessage(mt, msg); err != nil {
			log.Println("write:", err)
			return
		}

		// Cooperative cancellation if the request context dies.
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// adminStats is a tiny, separately rate-limited HTTP endpoint that demonstrates
// reusing the same package against a non-WebSocket request.
type adminStats struct {
	limiter *rl.SlidingWindow
}

func newAdminStats() *adminStats {
	return &adminStats{
		limiter: rl.NewSlidingWindow(10, time.Minute), // 10 calls per minute
	}
}

func (a *adminStats) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow() {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "rate-limited", http.StatusTooManyRequests)
		return
	}
	fmt.Fprintln(w, "ok")
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", echo)
	mux.Handle("/admin/stats", newAdminStats())

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("listening on :8080")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
