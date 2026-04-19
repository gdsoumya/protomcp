package protomcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
)

// TestResultProcessor_RunsOnSuccess verifies processors mutate a
// successful CallToolResult in registration order.
func TestResultProcessor_RunsOnSuccess(t *testing.T) {
	redactor := func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		if len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				tc.Text = strings.ReplaceAll(tc.Text, "secret", "***")
			}
		}
		return r, nil
	}

	s := New("t", "0.0.1", WithResultProcessor(redactor))
	final := func(_ context.Context, _ *mcp.CallToolRequest, _ *GRPCRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "the secret is 42"}}}, nil
	}

	result, _ := s.Chain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCRequest{Metadata: metadata.MD{}})
	out, _, err := s.FinishCall(context.Background(), &mcp.CallToolRequest{}, result, nil)
	if err != nil {
		t.Fatalf("FinishCall err: %v", err)
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "***") || strings.Contains(text, "secret") {
		t.Errorf("redaction failed: %q", text)
	}
}

// TestResultProcessor_RunsOnErrorResult verifies processors also run on
// IsError results synthesized by the ErrorHandler — so a redaction
// processor covers both success and failure paths with one rule.
func TestResultProcessor_RunsOnErrorResult(t *testing.T) {
	var seen int
	p := func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		seen++
		return r, nil
	}
	s := New("t", "0.0.1", WithResultProcessor(p))

	// Plain error → DefaultErrorHandler → IsError result → processor runs.
	out, _, err := s.FinishCall(context.Background(), &mcp.CallToolRequest{}, nil, errors.New("kaboom"))
	if err != nil {
		t.Fatalf("FinishCall returned err = %v, want nil (IsError result expected)", err)
	}
	if out == nil || !out.IsError {
		t.Fatalf("want IsError result, got %+v", out)
	}
	if seen != 1 {
		t.Errorf("processor ran %d times, want 1", seen)
	}
}

// TestResultProcessor_Order verifies processors run in registration
// order (each sees the output of the previous).
func TestResultProcessor_Order(t *testing.T) {
	appendTag := func(tag string) ResultProcessor {
		return func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
			tc := r.Content[0].(*mcp.TextContent)
			tc.Text += "|" + tag
			return r, nil
		}
	}
	s := New("t", "0.0.1",
		WithResultProcessor(appendTag("a"), appendTag("b")),
		WithResultProcessor(appendTag("c")),
	)

	in := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "start"}}}
	out, _, _ := s.FinishCall(context.Background(), &mcp.CallToolRequest{}, in, nil)
	if got := out.Content[0].(*mcp.TextContent).Text; got != "start|a|b|c" {
		t.Errorf("order wrong: %q", got)
	}
}

// TestResultProcessor_ErrorPropagates verifies a processor that returns
// a non-nil error surfaces as a JSON-RPC error (generated handlers
// return it directly; subsequent processors are skipped).
func TestResultProcessor_ErrorPropagates(t *testing.T) {
	boom := errors.New("processor exploded")
	failing := func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		return nil, boom
	}
	var downstreamRan bool
	downstream := func(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		downstreamRan = true
		return r, nil
	}
	s := New("t", "0.0.1", WithResultProcessor(failing, downstream))

	_, _, err := s.FinishCall(context.Background(), &mcp.CallToolRequest{},
		&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "x"}}}, nil)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if downstreamRan {
		t.Errorf("downstream processor should not run after error")
	}
}

// TestResultProcessor_NilReturnIsPassthrough verifies a processor that
// returns a nil *CallToolResult is treated as a no-op rather than
// wiping the response.
func TestResultProcessor_NilReturnIsPassthrough(t *testing.T) {
	noop := func(_ context.Context, _ *mcp.CallToolRequest, _ *mcp.CallToolResult) (*mcp.CallToolResult, error) {
		return nil, nil
	}
	s := New("t", "0.0.1", WithResultProcessor(noop))
	in := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "preserved"}}}
	out, _, err := s.FinishCall(context.Background(), &mcp.CallToolRequest{}, in, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Content[0].(*mcp.TextContent).Text != "preserved" {
		t.Errorf("processor wiped content: %+v", out)
	}
}
