// Tests that exercise the two newer seams — input mutation via
// GRPCRequest.Input and response redaction via ResultProcessor — through
// a full end-to-end tool call against the real gRPC server.
package greeter_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMiddleware_MutatesGRPCInput verifies a Middleware that rewrites
// the Name field of HelloRequest via g.Input — the rewritten value is
// what reaches the gRPC server and shows up in the echoed response.
func TestMiddleware_MutatesGRPCInput(t *testing.T) {
	grpcClient := startGRPC(t)

	rewrite := func(next protomcp.Handler) protomcp.Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
			if r, ok := g.Input.(*greeterv1.HelloRequest); ok {
				r.Name = "middleware-" + r.Name
			}
			return next(ctx, req, g)
		}
	}

	srv := protomcp.New("greeter", "0.1.0", protomcp.WithMiddleware(rewrite))
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)
	cs := connect(context.Background(), t, srv, nil)

	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.IsError {
		t.Fatalf("IsError: %+v", out)
	}
	var resp struct{ Message string }
	_ = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &resp)
	if resp.Message != "Hello, middleware-alice!" {
		t.Errorf("got %q, want %q", resp.Message, "Hello, middleware-alice!")
	}
}

// TestMiddleware_ReplacesGRPCInputPointer verifies that a Middleware
// which swaps g.Input for an entirely different message pointer is
// honored — the replacement is what reaches the gRPC server, not the
// originally-unmarshaled message. This exercises the g.Input read
// path on the generated handler (vs. a closure-captured &in which
// would silently ignore the swap).
func TestMiddleware_ReplacesGRPCInputPointer(t *testing.T) {
	grpcClient := startGRPC(t)

	swap := func(next protomcp.Handler) protomcp.Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
			g.Input = &greeterv1.HelloRequest{Name: "swapped"}
			return next(ctx, req, g)
		}
	}

	srv := protomcp.New("greeter", "0.1.0", protomcp.WithMiddleware(swap))
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)
	cs := connect(context.Background(), t, srv, nil)

	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: map[string]any{"name": "original"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.IsError {
		t.Fatalf("IsError: %+v", out)
	}
	var resp struct{ Message string }
	_ = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &resp)
	if resp.Message != "Hello, swapped!" {
		t.Errorf("got %q, want %q (replacement ignored?)", resp.Message, "Hello, swapped!")
	}
}

// TestResultProcessor_RedactsResponse verifies a ResultProcessor
// mutates the TextContent payload before the client observes it —
// a canonical "scrub a field from the response" use case.
func TestResultProcessor_RedactsResponse(t *testing.T) {
	grpcClient := startGRPC(t)

	redact := func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		for _, c := range r.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				tc.Text = strings.ReplaceAll(tc.Text, "Hello", "[redacted]")
			}
		}
		return r, nil
	}

	srv := protomcp.New("greeter", "0.1.0", protomcp.WithResultProcessor(redact))
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)
	cs := connect(context.Background(), t, srv, nil)

	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: map[string]any{"name": "world"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "[redacted]") || strings.Contains(text, "Hello") {
		t.Errorf("redaction did not apply: %q", text)
	}
}
