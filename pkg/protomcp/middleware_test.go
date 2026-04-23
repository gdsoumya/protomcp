package protomcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
)

// TestChainCompositionOrder verifies that the first ToolMiddleware passed
// to WithToolMiddleware is the outermost wrapper. Execution order must be
// m1-pre, m2-pre, final, m2-post, m1-post.
func TestChainCompositionOrder(t *testing.T) {
	var order []string

	mk := func(name string) ToolMiddleware {
		return func(next ToolHandler) ToolHandler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
				order = append(order, name+"-pre")
				res, err := next(ctx, req, g)
				order = append(order, name+"-post")
				return res, err
			}
		}
	}

	s := New("test", "0.0.1",
		WithToolMiddleware(mk("outer"), mk("inner")),
	)

	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		order = append(order, "final")
		return &mcp.CallToolResult{}, nil
	}

	if _, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"outer-pre", "inner-pre", "final", "inner-post", "outer-post"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestChainAppendsAcrossCalls verifies that multiple WithToolMiddleware
// options accumulate rather than replace.
func TestChainAppendsAcrossCalls(t *testing.T) {
	var order []string
	mk := func(name string) ToolMiddleware {
		return func(next ToolHandler) ToolHandler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
				order = append(order, name)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithToolMiddleware(mk("a")),
		WithToolMiddleware(mk("b"), mk("c")),
	)

	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		order = append(order, "final")
		return &mcp.CallToolResult{}, nil
	}

	if _, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"a", "b", "c", "final"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestChainShortCircuit verifies that a ToolMiddleware returning an error
// prevents downstream ToolMiddleware and the final ToolHandler from running.
func TestChainShortCircuit(t *testing.T) {
	sentinel := errors.New("stop")

	blocker := func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
			return nil, sentinel
		}
	}

	called := false
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{}, nil
	}

	s := New("t", "0.0.1", WithToolMiddleware(blocker))
	_, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if called {
		t.Fatalf("final handler should not have been called after short-circuit")
	}
}

// TestChainMetadataAccumulation verifies that each ToolMiddleware can write
// to the shared metadata.MD and the final ToolHandler observes the union.
func TestChainMetadataAccumulation(t *testing.T) {
	addKey := func(key, value string) ToolMiddleware {
		return func(next ToolHandler) ToolHandler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
				g.Metadata.Set(key, value)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithToolMiddleware(
			addKey("x-user", "alice"),
			addKey("x-tenant", "acme"),
		),
	)

	var seen metadata.MD
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		seen = g.Metadata
		return &mcp.CallToolResult{}, nil
	}

	md := metadata.MD{}
	if _, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: md}); err != nil {
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
// ToolMiddleware is registered: the final ToolHandler is invoked directly.
func TestChainNoMiddleware(t *testing.T) {
	s := New("t", "0.0.1")
	called := false
	final := func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{}, nil
	}
	if _, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	if !called {
		t.Fatalf("final was not called")
	}
}
