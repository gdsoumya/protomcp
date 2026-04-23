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
	redactor := func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
		r := data.Output
		if len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				tc.Text = strings.ReplaceAll(tc.Text, "secret", "***")
			}
		}
		return r, nil
	}

	s := New("t", "0.0.1", WithToolResultProcessor(redactor))
	final := func(_ context.Context, _ *mcp.CallToolRequest, _ *GRPCData) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "the secret is 42"}}}, nil
	}

	g := &GRPCData{Metadata: metadata.MD{}}
	result, _ := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, g)
	out, _, err := s.FinishToolCall(context.Background(), &mcp.CallToolRequest{}, g, result, nil)
	if err != nil {
		t.Fatalf("FinishCall err: %v", err)
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "***") || strings.Contains(text, "secret") {
		t.Errorf("redaction failed: %q", text)
	}
}

// TestResultProcessor_RunsOnErrorResult verifies processors also run on
// IsError results synthesized by the ToolErrorHandler, so a redaction
// processor covers both success and failure paths with one rule.
func TestResultProcessor_RunsOnErrorResult(t *testing.T) {
	var seen int
	p := func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
		seen++
		return data.Output, nil
	}
	s := New("t", "0.0.1", WithToolResultProcessor(p))

	// Plain error → DefaultToolErrorHandler → IsError result → processor runs.
	out, _, err := s.FinishToolCall(context.Background(), &mcp.CallToolRequest{}, nil, nil, errors.New("kaboom"))
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
	appendTag := func(tag string) ToolResultProcessor {
		return func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
			r := data.Output
			tc := r.Content[0].(*mcp.TextContent)
			tc.Text += "|" + tag
			return r, nil
		}
	}
	s := New("t", "0.0.1",
		WithToolResultProcessor(appendTag("a"), appendTag("b")),
		WithToolResultProcessor(appendTag("c")),
	)

	in := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "start"}}}
	out, _, _ := s.FinishToolCall(context.Background(), &mcp.CallToolRequest{}, nil, in, nil)
	if got := out.Content[0].(*mcp.TextContent).Text; got != "start|a|b|c" {
		t.Errorf("order wrong: %q", got)
	}
}

// TestResultProcessor_ErrorPropagates verifies a processor that returns
// a non-nil error surfaces as a JSON-RPC error (generated handlers
// return it directly; subsequent processors are skipped).
func TestResultProcessor_ErrorPropagates(t *testing.T) {
	boom := errors.New("processor exploded")
	failing := func(_ context.Context, _ *GRPCData, _ *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
		return nil, boom
	}
	var downstreamRan bool
	downstream := func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
		downstreamRan = true
		return data.Output, nil
	}
	s := New("t", "0.0.1", WithToolResultProcessor(failing, downstream))

	_, _, err := s.FinishToolCall(context.Background(), &mcp.CallToolRequest{}, nil,
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
	noop := func(_ context.Context, _ *GRPCData, _ *MCPData[*mcp.CallToolRequest, *mcp.CallToolResult]) (*mcp.CallToolResult, error) {
		return nil, nil
	}
	s := New("t", "0.0.1", WithToolResultProcessor(noop))
	in := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "preserved"}}}
	out, _, err := s.FinishToolCall(context.Background(), &mcp.CallToolRequest{}, nil, in, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Content[0].(*mcp.TextContent).Text != "preserved" {
		t.Errorf("processor wiped content: %+v", out)
	}
}
