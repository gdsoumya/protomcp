package protomcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// runChainWithError wires a single middleware that short-circuits with err
// into a fresh Server (optionally with a custom ToolErrorHandler) and drives
// the "middleware → Server.Chain → Server.HandleError" path the way
// generated code does. It returns what a real tool call would ultimately
// surface to the SDK.
func runChainWithError(t *testing.T, err error, opts ...ServerOption) (*mcp.CallToolResult, json.RawMessage, error) {
	t.Helper()
	mw := func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
			return nil, err
		}
	}
	opts = append([]ServerOption{WithToolMiddleware(mw)}, opts...)
	s := New("t", "0.0.1", opts...)

	final := func(ctx context.Context, _ *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		t.Fatalf("final handler should not run when middleware short-circuits")
		return nil, nil
	}
	_, cerr := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}})
	if cerr == nil {
		return nil, nil, nil
	}
	// runChainWithError keeps the three-tuple shape so callers can still
	// pattern-match on a future raw-payload return without touching every
	// assertion. Today HandleError returns just (result, err).
	res, herr := s.HandleToolError(context.Background(), &mcp.CallToolRequest{}, cerr)
	return res, nil, herr
}

// TestMiddlewareErrorThroughDefaultHandler_NotFound verifies a gRPC
// NotFound returned by a middleware is folded into an IsError result by
// DefaultToolErrorHandler, exercising the full middleware → Chain →
// HandleError path that generated code runs.
func TestMiddlewareErrorThroughDefaultHandler_NotFound(t *testing.T) {
	err := status.Error(codes.NotFound, "no such thing")
	res, raw, gotErr := runChainWithError(t, err)
	if gotErr != nil {
		t.Fatalf("err = %v, want nil (NotFound should fold into IsError result)", gotErr)
	}
	if raw != nil {
		t.Fatalf("raw = %v, want nil", raw)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "NotFound") || !strings.Contains(tc.Text, "no such thing") {
		t.Fatalf("text = %q, want to contain code + message", tc.Text)
	}
}

// TestMiddlewareErrorThroughDefaultHandler_PlainError verifies a plain
// Go error returned by a middleware folds into an IsError result with
// the raw message, still routed through Server.HandleError.
func TestMiddlewareErrorThroughDefaultHandler_PlainError(t *testing.T) {
	res, raw, gotErr := runChainWithError(t, errors.New("rejected by policy"))
	if gotErr != nil {
		t.Fatalf("err = %v, want nil", gotErr)
	}
	if raw != nil {
		t.Fatalf("raw = %v, want nil", raw)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	if tc.Text != "rejected by policy" {
		t.Fatalf("text = %q, want %q", tc.Text, "rejected by policy")
	}
}

// TestMiddlewareErrorThroughDefaultHandler_Unauthenticated verifies that
// a gRPC Unauthenticated from middleware is surfaced as a JSON-RPC
// protocol error (i.e. a *jsonrpc.Error) rather than being folded into
// an IsError result. HandleError returns (nil, nil, jsonrpc.Error).
func TestMiddlewareErrorThroughDefaultHandler_Unauthenticated(t *testing.T) {
	err := status.Error(codes.Unauthenticated, "nope")
	res, raw, gotErr := runChainWithError(t, err)
	if res != nil {
		t.Fatalf("res = %+v, want nil for Unauthenticated", res)
	}
	if raw != nil {
		t.Fatalf("raw = %v, want nil", raw)
	}
	if gotErr == nil {
		t.Fatalf("expected non-nil error for Unauthenticated")
	}
	// DefaultToolErrorHandler must wrap the auth codes in *jsonrpc.Error so
	// the SDK surfaces them as protocol-level errors.
	var je *jsonrpc.Error
	if !errors.As(gotErr, &je) {
		t.Fatalf("err = %T, want *jsonrpc.Error", gotErr)
	}
	if !strings.Contains(je.Message, "Unauthenticated") {
		t.Errorf("jsonrpc.Error.Message = %q, want to contain \"Unauthenticated\"", je.Message)
	}
}

// TestCustomErrorHandlerHandlesMiddlewareError verifies that a custom
// ToolErrorHandler, not DefaultToolErrorHandler, is the one invoked when a
// middleware short-circuits with an error.
func TestCustomErrorHandlerHandlesMiddlewareError(t *testing.T) {
	var (
		called  bool
		sawErr  error
		sawAuth = errors.New("synthetic middleware failure")
	)
	custom := func(_ context.Context, _ *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
		called = true
		sawErr = err
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "custom-mw:" + err.Error()}},
		}, nil
	}

	res, _, gotErr := runChainWithError(t, sawAuth, WithToolErrorHandler(custom))
	if !called {
		t.Fatalf("custom handler was not invoked")
	}
	if sawErr != sawAuth {
		t.Fatalf("custom handler saw err = %v, want %v", sawErr, sawAuth)
	}
	if gotErr != nil {
		t.Fatalf("gotErr = %v, want nil (custom returned a result)", gotErr)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	if tc := res.Content[0].(*mcp.TextContent); tc.Text != "custom-mw:synthetic middleware failure" {
		t.Fatalf("text = %q", tc.Text)
	}
}

// TestCustomErrorHandlerHandlesInnerHandlerError verifies that the
// custom ToolErrorHandler also runs when the innermost ToolHandler (supplied by
// generated code) returns the error, not just when middleware does.
// Generated code funnels both through Server.HandleError, this test is
// the direct simulation of that path.
func TestCustomErrorHandlerHandlesInnerHandlerError(t *testing.T) {
	var called bool
	custom := func(_ context.Context, _ *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
		called = true
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "custom-inner:" + err.Error()}},
		}, nil
	}
	s := New("t", "0.0.1", WithToolErrorHandler(custom))

	inner := errors.New("inner kaboom")
	final := func(_ context.Context, _ *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		return nil, inner
	}
	_, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}})
	if err == nil {
		t.Fatalf("expected chain to surface inner error")
	}
	res, gotErr := s.HandleToolError(context.Background(), &mcp.CallToolRequest{}, err)
	if !called {
		t.Fatalf("custom handler was not invoked for inner error")
	}
	if gotErr != nil {
		t.Fatalf("gotErr = %v, want nil", gotErr)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	if tc := res.Content[0].(*mcp.TextContent); tc.Text != "custom-inner:inner kaboom" {
		t.Fatalf("text = %q", tc.Text)
	}
}

// TestChainThreeMiddlewareMetadataAccumulation verifies that three
// stacked middlewares each writing a distinct metadata key are all
// observed by the innermost ToolHandler, proving metadata.MD threads through
// the full chain rather than being shadowed by an intermediate layer.
func TestChainThreeMiddlewareMetadataAccumulation(t *testing.T) {
	var order []string
	add := func(name, key, val string) ToolMiddleware {
		return func(next ToolHandler) ToolHandler {
			return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
				order = append(order, name)
				g.Metadata.Set(key, val)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithToolMiddleware(
			add("a", "x-a", "1"),
			add("b", "x-b", "2"),
			add("c", "x-c", "3"),
		),
	)

	var seen metadata.MD
	final := func(_ context.Context, _ *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
		seen = g.Metadata
		return &mcp.CallToolResult{}, nil
	}

	if _, err := s.ToolChain(final)(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain: %v", err)
	}

	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v, want [a b c]", order)
	}
	for _, kv := range [][2]string{{"x-a", "1"}, {"x-b", "2"}, {"x-c", "3"}} {
		if got := seen.Get(kv[0]); len(got) != 1 || got[0] != kv[1] {
			t.Fatalf("%s = %v, want [%s]", kv[0], got, kv[1])
		}
	}
}

// TestChainNilFinalNoMiddleware documents the shape of Server.Chain when
// given a nil final ToolHandler. With no middleware the composition loop is
// a no-op and the returned ToolHandler is itself nil, invoking it would
// nil-function-call panic, so callers must never pass nil. This test
// pins the documented behavior so a future refactor that silently
// changes it is caught.
func TestChainNilFinalNoMiddleware(t *testing.T) {
	s := New("t", "0.0.1")
	got := s.ToolChain(nil)
	if got != nil {
		t.Fatalf("Chain(nil) with no middleware = %v, want nil", got)
	}
}

// TestChainNilFinalWithMiddlewarePanics documents the Chain(nil)
// contract when middleware is configured: the first middleware that
// actually calls next(ctx, req, g) will encounter a nil ToolHandler and
// nil-function-call panic at invocation time. ToolMiddleware that
// short-circuits without calling next remains safe. Callers must never
// pass nil as the final ToolHandler; generated code always supplies one.
func TestChainNilFinalWithMiddlewarePanics(t *testing.T) {
	// A passthrough middleware that calls next, this is what exposes
	// the nil ToolHandler.
	mw := func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData) (*mcp.CallToolResult, error) {
			return next(ctx, req, g)
		}
	}
	s := New("t", "0.0.1", WithToolMiddleware(mw))
	chain := s.ToolChain(nil)
	if chain == nil {
		t.Fatalf("Chain(nil) with middleware must still return a non-nil composed ToolHandler")
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when passthrough middleware invokes nil final")
		}
	}()
	_, _ = chain(context.Background(), &mcp.CallToolRequest{}, &GRPCData{Metadata: metadata.MD{}})
}

// TestMustParseSchemaPanicMessageMentionsPackage verifies the panic
// message from MustParseSchema is clearly attributable to protomcp ,
// generated code uses it at package init so failures must be easy to
// locate.
func TestMustParseSchemaPanicMessageMentionsPackage(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("recovered value is %T, want string", r)
		}
		if !strings.Contains(msg, "protomcp") || !strings.Contains(msg, "parse schema") {
			t.Fatalf("panic message %q should mention \"protomcp\" and \"parse schema\"", msg)
		}
	}()
	_ = MustParseSchema(`{"type": "object", "properties": [[[`) // malformed JSON
}
