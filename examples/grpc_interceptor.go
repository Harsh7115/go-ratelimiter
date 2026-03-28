// grpc_interceptor.go — gRPC unary server interceptor using go-ratelimiter
//
// Demonstrates wrapping a gRPC server with per-client-IP token-bucket rate
// limiting.  Each unique client IP gets its own limiter; a shared limiter
// also caps overall throughput on the server.
//
// Usage:
//
//	go run ./examples/grpc_interceptor.go
//
// The example starts a gRPC server on :50051 with a toy EchoService.
// Requests exceeding 10 RPS per client, or 100 RPS globally, receive
// a gRPC RESOURCE_EXHAUSTED error.

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	ratelimiter "github.com/Harsh7115/go-ratelimiter"
)

// ---------------------------------------------------------------------------
// Toy EchoService (normally generated from a .proto file)
// ---------------------------------------------------------------------------

type EchoRequest struct{ Message string }
type EchoResponse struct{ Message string }

type EchoServiceServer interface {
	Echo(context.Context, *EchoRequest) (*EchoResponse, error)
}

type echoServer struct{}

func (e *echoServer) Echo(_ context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: "echo: " + req.Message}, nil
}

// ---------------------------------------------------------------------------
// Rate-limiter state
// ---------------------------------------------------------------------------

// perClientLimiters keeps one TokenBucket limiter per client IP.
// In production, add a background goroutine to evict stale entries.
type perClientLimiters struct {
	mu       sync.Mutex
	limiters map[string]*ratelimiter.TokenBucket
	rate     float64 // tokens per second per client
	burst    int     // max burst per client
}

func newPerClientLimiters(ratePerSec float64, burst int) *perClientLimiters {
	return &perClientLimiters{
		limiters: make(map[string]*ratelimiter.TokenBucket),
		rate:     ratePerSec,
		burst:    burst,
	}
}

func (p *perClientLimiters) get(clientIP string) *ratelimiter.TokenBucket {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.limiters[clientIP]; ok {
		return l
	}
	l := ratelimiter.NewTokenBucket(p.rate, p.burst)
	p.p.limiters[clientIP] = l
	return l
}

// ---------------------------------------------------------------------------
// Interceptor factory
// ---------------------------------------------------------------------------

// RateLimitInterceptor returns a gRPC UnaryServerInterceptor that enforces:
//   - globalLimiter: a shared cap across all clients combined
//   - perClient:     per-source-IP cap to prevent individual clients hogging quota
//
// Both limiters use token-bucket semantics.  The interceptor rejects requests
// immediately (no queuing) when either bucket is empty.
func RateLimitInterceptor(
	globalLimiter *ratelimiter.TokenBucket,
	perClient *perClientLimiters,
) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		// 1. Global rate check
		if !globalLimiter.Allow() {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"global rate limit exceeded on %s",
				info.FullMethod,
			)
		}

		// 2. Per-client rate check
		clientIP := extractClientIP(ctx)
		if !perClient.get(clientIP).Allow() {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"per-client rate limit exceeded for %s on %s",
				clientIP,
				info.FullMethod,
			)
		}

		return handler(ctx, req)
	}
}

// extractClientIP returns the remote address host from the gRPC peer info,
// falling back to "unknown" if the peer is not available.
func extractClientIP(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err == nil {
			return host
		}
		return p.Addr.String()
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// main — wire everything together and start the server
// ---------------------------------------------------------------------------

func main() {
	const addr = ":50051"

	// Global limiter: 100 RPS with burst of 20
	globalLimiter := ratelimiter.NewTokenBucket(100, 20)

	// Per-client limiter: 10 RPS with burst of 5
	perClient := newPerClientLimiters(10, 5)

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(RateLimitInterceptor(globalLimiter, perClient)),
	)

	// Register the echo service (normally via generated RegisterXxxServer)
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*EchoServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Echo",
				Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
					req := new(EchoRequest)
					if err := dec(req); err != nil {
						return nil, err
					}
					return srv.(EchoServiceServer).Echo(ctx, req)
				},
			},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "echo.proto",
	}, &echoServer{})

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	fmt.Printf("gRPC server listening on %s\n", addr)
	fmt.Printf("  global limit : 100 RPS (burst 20)\n")
	fmt.Printf("  per-client   : 10 RPS (burst 5)\n")

	// Demonstrate the limiter before blocking on Serve.
	go func() {
		time.Sleep(100 * time.Millisecond)
		fmt.Printf("Server ready — connect with a gRPC client to :50051\n")
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
