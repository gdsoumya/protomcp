package protomcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
)

// TestChainCompositionOrder verifies that the first Middleware passed
// to WithMiddleware is the outermost wrapper. Execution order must be
// m1-pre, m2-pre, final, m2-post, m1-post.
func TestChainCompositionOrder(t *testing.T) {
	var order []string

	mk := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
				order = append(order, name+"-pre")
				res, err := next(ctx, req, g)
				order = append(order, name+"-post")
				return res, err
			}
		}
	}

	s := New("test", "0.0.1",
		WithMiddleware(mk("outer"), mk("inner")),
	)

	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		order = append(order, "final")
		return &mcp.CallToolResult{}, nil
	}

	if _, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"outer-pre", "inner-pre", "final", "inner-post", "outer-post"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestChainAppendsAcrossCalls verifies that multiple WithMiddleware
// options accumulate rather than replace.
func TestChainAppendsAcrossCalls(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
				order = append(order, name)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithMiddleware(mk("a")),
		WithMiddleware(mk("b"), mk("c")),
	)

	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		order = append(order, "final")
		return &mcp.CallToolResult{}, nil
	}

	if _, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"a", "b", "c", "final"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestChainShortCircuit verifies that a Middleware returning an error
// prevents downstream Middleware and the final Handler from running.
func TestChainShortCircuit(t *testing.T) {
	sentinel := errors.New("stop")

	blocker := func(next Handler) Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
			return nil, sentinel
		}
	}

	called := false
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{}, nil
	}

	s := New("t", "0.0.1", WithMiddleware(blocker))
	_, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: metadata.MD{}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if called {
		t.Fatalf("final handler should not have been called after short-circuit")
	}
}

// TestChainMetadataAccumulation verifies that each Middleware can write
// to the shared metadata.MD and the final Handler observes the union.
func TestChainMetadataAccumulation(t *testing.T) {
	addKey := func(key, value string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
				g.Metadata.Set(key, value)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithMiddleware(
			addKey("x-user", "alice"),
			addKey("x-tenant", "acme"),
		),
	)

	var seen metadata.MD
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		seen = g.Metadata
		return &mcp.CallToolResult{}, nil
	}

	md := metadata.MD{}
	if _, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: md}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	if got := seen.Get("x-user"); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("x-user = %v, want [alice]", got)
	}
	if got := seen.Get("x-tenant"); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("x-tenant = %v, want [acme]", got)
	}
}

// TestChainNoMiddleware verifies the chain is a no-op when no
// Middleware is registered: the final Handler is invoked directly.
func TestChainNoMiddleware(t *testing.T) {
	s := New("t", "0.0.1")
	called := false
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{}, nil
	}
	if _, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	if !called {
		t.Fatalf("final was not called")
	}
}
