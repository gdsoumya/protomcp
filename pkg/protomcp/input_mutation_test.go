package protomcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// TestMiddleware_MutatesInputViaTypeAssertion verifies that a Middleware
// can type-assert g.Input to a concrete proto message and mutate it, and
// that the mutation is visible to code that runs after the chain — i.e.,
// the pointer semantics hold. The common case is injecting a tenant ID
// from auth context into the request body.
func TestMiddleware_MutatesInputViaTypeAssertion(t *testing.T) {
	// Use ToolOptions from our own generated annotations package as a
	// convenient proto.Message we know is importable in tests.
	in := &protomcpv1.ToolOptions{}

	injector := func(next Handler) Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
			if to, ok := g.Input.(*protomcpv1.ToolOptions); ok {
				to.Title = "injected by middleware"
			}
			return next(ctx, req, g)
		}
	}

	s := New("t", "0.0.1", WithMiddleware(injector))

	var observed string
	final := func(_ context.Context, _ *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		observed = g.Input.(*protomcpv1.ToolOptions).GetTitle()
		return &mcp.CallToolResult{}, nil
	}

	_, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{
		Input:    in,
		Metadata: metadata.MD{},
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if observed != "injected by middleware" {
		t.Errorf("final observed Title = %q, want %q", observed, "injected by middleware")
	}
	// The original pointer must reflect the mutation too — the generated
	// tool handler closes over that pointer to make the upstream call.
	if in.GetTitle() != "injected by middleware" {
		t.Errorf("original pointer not mutated: %q", in.GetTitle())
	}
}

// TestMiddleware_MutatesInputViaReflection verifies generic middleware
// can set fields through proto reflection without knowing the concrete
// type at compile time. This is what a reusable "inject tenant on any
// message with a 'tenant_id' field" middleware would do.
func TestMiddleware_MutatesInputViaReflection(t *testing.T) {
	in := &protomcpv1.ToolOptions{}

	genericInjector := func(next Handler) Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
			if g.Input == nil {
				return next(ctx, req, g)
			}
			m := g.Input.ProtoReflect()
			if fd := m.Descriptor().Fields().ByName("title"); fd != nil && fd.Kind() == protoreflect.StringKind {
				m.Set(fd, protoreflect.ValueOfString("reflection-set"))
			}
			return next(ctx, req, g)
		}
	}

	s := New("t", "0.0.1", WithMiddleware(genericInjector))
	final := func(_ context.Context, _ *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	_, err := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{
		Input:    in,
		Metadata: metadata.MD{},
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if in.GetTitle() != "reflection-set" {
		t.Errorf("reflection mutation failed: Title=%q", in.GetTitle())
	}
}

// TestMiddleware_InputPointerMatchesGeneratedHandler documents the
// contract that the pointer stored in GRPCRequest.Input IS the pointer
// the generated handler uses for the upstream call. The test verifies
// pointer identity (same memory) rather than equal values.
func TestMiddleware_InputPointerMatchesGeneratedHandler(t *testing.T) {
	original := &protomcpv1.ToolOptions{}
	g := &GRPCRequest{Input: original, Metadata: metadata.MD{}}

	var seen proto.Message
	mw := func(next Handler) Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error) {
			seen = g.Input
			return next(ctx, req, g)
		}
	}
	s := New("t", "0.0.1", WithMiddleware(mw))
	final := func(_ context.Context, _ *mcp.CallToolRequest, _ *GRPCRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	_, _ = s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, g)

	// Pointer equality: seen is the same &ToolOptions{} we passed in.
	if seen != proto.Message(original) {
		t.Errorf("middleware saw different pointer than provided")
	}
}
