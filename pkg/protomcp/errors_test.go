package protomcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestDefaultErrorHandlerAuthEscalates verifies that Unauthenticated /
// PermissionDenied / Canceled / DeadlineExceeded gRPC statuses are
// surfaced as JSON-RPC errors wrapped in *jsonrpc.Error — the SDK only
// treats an error as a protocol-level failure when it is a *jsonrpc.Error;
// any other error type gets folded into an IsError CallToolResult.
func TestDefaultErrorHandlerAuthEscalates(t *testing.T) {
	cases := []codes.Code{
		codes.Unauthenticated,
		codes.PermissionDenied,
		codes.Canceled,
		codes.DeadlineExceeded,
	}
	for _, c := range cases {
		t.Run(c.String(), func(t *testing.T) {
			err := status.Error(c, "nope")
			res, gotErr := DefaultErrorHandler(context.Background(), &mcp.CallToolRequest{}, err)
			if res != nil {
				t.Fatalf("expected nil result for %s, got %+v", c, res)
			}
			if gotErr == nil {
				t.Fatalf("expected non-nil error for %s", c)
			}
			var je *jsonrpc.Error
			if !errors.As(gotErr, &je) {
				t.Fatalf("err = %T, want *jsonrpc.Error", gotErr)
			}
			if !strings.Contains(je.Message, c.String()) {
				t.Errorf("jsonrpc.Error.Message = %q, want to contain %q", je.Message, c.String())
			}
			if !strings.Contains(je.Message, "nope") {
				t.Errorf("jsonrpc.Error.Message = %q, want to contain %q", je.Message, "nope")
			}
		})
	}
}

// TestDefaultErrorHandlerOtherGRPCStatus verifies that non-auth gRPC
// statuses (e.g. NotFound) are folded into a CallToolResult with
// IsError=true, a human-readable text content, and a structured content
// carrying the serialized google.rpc.Status.
func TestDefaultErrorHandlerOtherGRPCStatus(t *testing.T) {
	err := status.Error(codes.NotFound, "missing widget")
	res, gotErr := DefaultErrorHandler(context.Background(), &mcp.CallToolRequest{}, err)
	if gotErr != nil {
		t.Fatalf("expected nil error, got %v", gotErr)
	}
	if res == nil {
		t.Fatalf("expected non-nil result")
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if len(res.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "NotFound") || !strings.Contains(tc.Text, "missing widget") {
		t.Fatalf("text = %q, want to contain code and message", tc.Text)
	}
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want json.RawMessage", res.StructuredContent)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("StructuredContent not valid JSON: %v", err)
	}
}

// TestDefaultErrorHandlerPlainError verifies plain Go errors become a
// CallToolResult with IsError=true and the error message as text.
func TestDefaultErrorHandlerPlainError(t *testing.T) {
	err := errors.New("boom")
	res, gotErr := DefaultErrorHandler(context.Background(), &mcp.CallToolRequest{}, err)
	if gotErr != nil {
		t.Fatalf("expected nil error, got %v", gotErr)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	if tc.Text != "boom" {
		t.Fatalf("text = %q, want %q", tc.Text, "boom")
	}
	if res.StructuredContent != nil {
		t.Fatalf("StructuredContent = %v, want nil for plain error", res.StructuredContent)
	}
}

// TestServerHandleErrorAdaptsShapes verifies Server.HandleError routes
// each error shape to the expected return values.
func TestServerHandleErrorAdaptsShapes(t *testing.T) {
	s := New("t", "0.0.1")

	// Plain error path: expect a CallToolResult, nil err.
	res, err := s.HandleError(context.Background(), &mcp.CallToolRequest{}, errors.New("bad"))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}

	// Auth error path: expect no result but a JSON-RPC error.
	authErr := status.Error(codes.Unauthenticated, "deny")
	res, err = s.HandleError(context.Background(), &mcp.CallToolRequest{}, authErr)
	if res != nil {
		t.Fatalf("res = %+v, want nil", res)
	}
	if err == nil {
		t.Fatalf("err = nil, want non-nil")
	}

	// Nil error path: both returns nil.
	res, err = s.HandleError(context.Background(), &mcp.CallToolRequest{}, nil)
	if res != nil || err != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", res, err)
	}
}

// TestWithErrorHandlerOverrides verifies a custom ErrorHandler is
// preferred over the default.
func TestWithErrorHandlerOverrides(t *testing.T) {
	called := false
	custom := func(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "custom"}}}, nil
	}
	s := New("t", "0.0.1", WithErrorHandler(custom))
	res, err := s.HandleError(context.Background(), &mcp.CallToolRequest{}, errors.New("x"))
	if !called {
		t.Fatalf("custom handler not called")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	tc := res.Content[0].(*mcp.TextContent)
	if tc.Text != "custom" {
		t.Fatalf("text = %q, want custom", tc.Text)
	}
}
