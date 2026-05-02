// distributed_ratelimit.go — example of coordinating multiple rate limiters
// across a set of "nodes" using a shared atomic counter to simulate
// a distributed token-bucket without an external store.
//
// This is a single-process approximation: in production you would replace
// sharedTokens with a Redis INCR / Lua script, or a gossip-based counter.
// The pattern shown here (local pre-check → shared debit → rollback on fail)
// is the same regardless of the backing store.
//
// Usage:
//   go run examples/distributed_ratelimit.go

package main

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// SharedPool holds a global token count that all nodes draw from.
type SharedPool struct {
	tokens   int64
	capacity int64
	rate     float64
	mu       sync.Mutex
	lastFill time.Time
}

func NewSharedPool(capacity int64, rate float64) *SharedPool {
	return &SharedPool{
		tokens:   capacity,
		capacity: capacity,
		rate:     rate,
		lastFill: time.Now(),
	}
}

func (p *SharedPool) refill() {
	now := time.Now()
	elapsed := now.Sub(p.lastFill).Seconds()
	p.lastFill = now
	add := int64(elapsed * p.rate)
	if add <= 0 {
		return
	}
	cur := atomic.LoadInt64(&p.tokens)
	newVal := cur + add
	if newVal > p.capacity {
		newVal = p.capacity
	}
	atomic.StoreInt64(&p.tokens, newVal)
}

func (p *SharedPool) TryConsume(n int64) bool {
	p.mu.Lock()
	p.refill()
	p.mu.Unlock()
	for {
		cur := atomic.LoadInt64(&p.tokens)
		if cur < n {
			return false
		}
		if atomic.CompareAndSwapInt64(&p.tokens, cur, cur-n) {
			return true
		}
	}
}

func (p *SharedPool) Available() int64 {
	return atomic.LoadInt64(&p.tokens)
}

// Node simulates one application instance.
type Node struct {
	id   int
	pool *SharedPool
}

func NewNode(id int, pool *SharedPool) *Node {
	return &Node{id: id, pool: pool}
}

func (n *Node) Allow() bool {
	if n.pool.Available() == 0 {
		return false
	}
	return n.pool.TryConsume(1)
}

func main() {
	const (
		capacity   int64   = 200
		fillRate   float64 = 50
		numNodes           = 5
		duration           = 3 * time.Second
		rpsPerNode         = 40
	)

	pool := NewSharedPool(capacity, fillRate)
	nodes := make([]*Node, numNodes)
	for i := range nodes {
		nodes[i] = NewNode(i+1, pool)
	}

	type result struct {
		nodeID   int
		allowed  int
		rejected int
	}
	results := make([]result, numNodes)
	var wg sync.WaitGroup
	start := time.Now()

	for i, node := range nodes {
		wg.Add(1)
		go func(idx int, n *Node) {
			defer wg.Done()
			ticker := time.NewTicker(time.Second / time.Duration(rpsPerNode))
			defer ticker.Stop()
			for time.Since(start) < duration {
				<-ticker.C
				time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
				if n.Allow() {
					results[idx].allowed++
				} else {
					results[idx].rejected++
				}
				results[idx].nodeID = n.id
			}
		}(i, node)
	}
	wg.Wait()

	fmt.Printf("%-8s  %-10s  %-10s  %-12s
", "Node", "Allowed", "Rejected", "Accept Rate")
	fmt.Printf("%-8s  %-10s  %-10s  %-12s
", "----", "-------", "--------", "-----------")
	totalAllowed, totalRejected := 0, 0
	for _, r := range results {
		total := r.allowed + r.rejected
		rate := 0.0
		if total > 0 {
			rate = float64(r.allowed) / float64(total) * 100
		}
		fmt.Printf("%-8d  %-10d  %-10d  %10.1f%%
", r.nodeID, r.allowed, r.rejected, rate)
		totalAllowed += r.allowed
		totalRejected += r.rejected
	}
	fmt.Println()
	totalReqs := totalAllowed + totalRejected
	fmt.Printf("Total requests : %d
", totalReqs)
	fmt.Printf("Total allowed  : %d  (%.1f%%)
", totalAllowed, float64(totalAllowed)/float64(totalReqs)*100)
	fmt.Printf("Total rejected : %d  (%.1f%%)
", totalRejected, float64(totalRejected)/float64(totalReqs)*100)
	fmt.Printf("Effective RPS  : %.1f req/s allowed (budget %.0f/s)
",
		float64(totalAllowed)/duration.Seconds(), fillRate)
}
