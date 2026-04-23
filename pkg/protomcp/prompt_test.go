package protomcp

import (
	"context"
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

// TestPromptChainOrder verifies that the first PromptMiddleware passed
// to WithPromptMiddleware is the outermost wrapper. Execution order must
// match the tool-side semantics: m1-pre, m2-pre, final, m2-post, m1-post.
func TestPromptChainOrder(t *testing.T) {
	var order []string
	mk := func(name string) PromptMiddleware {
		return func(next PromptHandler) PromptHandler {
			return func(ctx context.Context, req *mcp.GetPromptRequest, g *GRPCData) (*mcp.GetPromptResult, error) {
				order = append(order, name+"-pre")
				res, err := next(ctx, req, g)
				order = append(order, name+"-post")
				return res, err
			}
		}
	}

	s := New("t", "0.0.1",
		WithPromptMiddleware(mk("outer"), mk("inner")),
	)

	final := func(_ context.Context, _ *mcp.GetPromptRequest, _ *GRPCData) (*mcp.GetPromptResult, error) {
		order = append(order, "final")
		return &mcp.GetPromptResult{}, nil
	}

	if _, err := s.PromptChain(final)(context.Background(), &mcp.GetPromptRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"outer-pre", "inner-pre", "final", "inner-post", "outer-post"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestPromptChainAppendsAcrossCalls verifies multiple WithPromptMiddleware
// options accumulate rather than replace.
func TestPromptChainAppendsAcrossCalls(t *testing.T) {
	var order []string
	mk := func(name string) PromptMiddleware {
		return func(next PromptHandler) PromptHandler {
			return func(ctx context.Context, req *mcp.GetPromptRequest, g *GRPCData) (*mcp.GetPromptResult, error) {
				order = append(order, name)
				return next(ctx, req, g)
			}
		}
	}

	s := New("t", "0.0.1",
		WithPromptMiddleware(mk("a")),
		WithPromptMiddleware(mk("b"), mk("c")),
	)

	final := func(_ context.Context, _ *mcp.GetPromptRequest, _ *GRPCData) (*mcp.GetPromptResult, error) {
		order = append(order, "final")
		return &mcp.GetPromptResult{}, nil
	}
	if _, err := s.PromptChain(final)(context.Background(), &mcp.GetPromptRequest{}, &GRPCData{Metadata: metadata.MD{}}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"a", "b", "c", "final"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// TestPromptMiddlewareNilPanics asserts WithPromptMiddleware fails at
// construction time when handed a nil entry, matching the tool-side
// nil-panic contract.
func TestPromptMiddlewareNilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from WithPromptMiddleware(nil)")
		}
	}()
	New("t", "0.0.1", WithPromptMiddleware(nil))
}

// TestPromptResultProcessorNilPanics asserts WithPromptResultProcessor
// fails at construction time when handed a nil entry.
func TestPromptResultProcessorNilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from WithPromptResultProcessor(nil)")
		}
	}()
	New("t", "0.0.1", WithPromptResultProcessor(nil))
}

// TestFinishPromptGetErrorRouting asserts errors are routed through the
// PromptErrorHandler and surfaced to the client as a *jsonrpc.Error via
// the shared grpcErrorToJSONRPC mapping (so prompts match the tool and
// resource surfaces for the same upstream gRPC error).
func TestFinishPromptGetErrorRouting(t *testing.T) {
	s := New("t", "0.0.1")
	_, err := s.FinishPromptGet(context.Background(), &mcp.GetPromptRequest{}, nil, nil,
		errors.New("boom"))
	if err == nil {
		t.Fatalf("err = nil, want non-nil")
	}
	var jrpcErr *jsonrpc.Error
	if !errors.As(err, &jrpcErr) {
		t.Fatalf("err = %v (%T); want *jsonrpc.Error", err, err)
	}
	if jrpcErr.Code != -32000 {
		t.Errorf("jsonrpc code = %d, want -32000 for non-gRPC error", jrpcErr.Code)
	}
	if !strings.Contains(jrpcErr.Message, "boom") {
		t.Errorf("jsonrpc message = %q, want to contain %q", jrpcErr.Message, "boom")
	}
}

// TestDefaultPromptErrorHandler_GRPCStatus asserts gRPC status errors
// map to the primitive-consistent JSON-RPC code table (mirrors the
// tool / resource defaults).
func TestDefaultPromptErrorHandler_GRPCStatus(t *testing.T) {
	grpcErr := status.Error(codes.PermissionDenied, "forbidden")
	_, err := DefaultPromptErrorHandler(context.Background(), &mcp.GetPromptRequest{}, grpcErr)
	var jrpcErr *jsonrpc.Error
	if !errors.As(err, &jrpcErr) {
		t.Fatalf("err = %v (%T); want *jsonrpc.Error", err, err)
	}
	if got, want := jrpcErr.Code, grpcCodeToJSONRPCCode(codes.PermissionDenied); got != want {
		t.Errorf("jsonrpc code = %d, want %d", got, want)
	}
	if !strings.Contains(jrpcErr.Message, "PermissionDenied") ||
		!strings.Contains(jrpcErr.Message, "forbidden") {
		t.Errorf("jsonrpc message = %q, want to carry both code and description", jrpcErr.Message)
	}
}

// TestFinishPromptGetProcessorsRun asserts PromptResultProcessors run in
// registration order on the successful path.
func TestFinishPromptGetProcessorsRun(t *testing.T) {
	var order []string
	p := func(name string) PromptResultProcessor {
		return func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.GetPromptRequest, *mcp.GetPromptResult]) (*mcp.GetPromptResult, error) {
			order = append(order, name)
			return data.Output, nil
		}
	}
	s := New("t", "0.0.1", WithPromptResultProcessor(p("first"), p("second")))
	result := &mcp.GetPromptResult{}
	got, err := s.FinishPromptGet(context.Background(), &mcp.GetPromptRequest{}, nil, result, nil)
	if err != nil {
		t.Fatalf("FinishPromptGet: %v", err)
	}
	if got != result {
		t.Fatalf("FinishPromptGet returned a different result pointer than it was handed")
	}
	want := []string{"first", "second"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("processor order = %v, want %v", order, want)
	}
}

// TestCompletePromptArgPrefixMatch exercises the static completion table
// registered by generated code. It verifies prefix filtering, the
// MCP 100-item cap, and the absence of entries for unknown
// prompt/argument pairs.
func TestCompletePromptArgPrefixMatch(t *testing.T) {
	s := New("t", "0.0.1")
	s.RegisterPromptArgCompletions("tasks_review", "status", []string{"TODO", "DOING", "DONE"})

	req := &mcp.CompleteRequest{
		Params: &mcp.CompleteParams{
			Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "tasks_review"},
			Argument: mcp.CompleteParamsArgument{
				Name:  "status",
				Value: "DO",
			},
		},
	}
	got := s.completePromptArg(req)
	if got == nil {
		t.Fatal("completePromptArg returned nil")
	}
	want := []string{"DOING", "DONE"}
	if !reflect.DeepEqual(got.Completion.Values, want) {
		t.Fatalf("values = %v, want %v", got.Completion.Values, want)
	}
	if got.Completion.Total != 2 {
		t.Fatalf("total = %d, want 2", got.Completion.Total)
	}

	// Unknown prompt → empty values list (not nil).
	req.Params.Ref.Name = "does-not-exist"
	got = s.completePromptArg(req)
	if got == nil || got.Completion.Values == nil {
		t.Fatal("values must be a non-nil empty slice for unknown prompt")
	}
	if len(got.Completion.Values) != 0 {
		t.Fatalf("expected empty values, got %v", got.Completion.Values)
	}

	// Non-prompt refs are ignored.
	req.Params.Ref = &mcp.CompleteReference{Type: "ref/resource", URI: "x://y"}
	got = s.completePromptArg(req)
	if got == nil || len(got.Completion.Values) != 0 {
		t.Fatalf("expected empty values for non-prompt ref, got %+v", got)
	}
}

// TestCompletePromptArgCaps verifies that a >100 value set is truncated
// and HasMore is flagged per MCP spec.
func TestCompletePromptArgCaps(t *testing.T) {
	s := New("t", "0.0.1")
	values := make([]string, 150)
	for i := range values {
		values[i] = "v" // all identical prefix so every value matches
	}
	s.RegisterPromptArgCompletions("p", "a", values)

	req := &mcp.CompleteRequest{
		Params: &mcp.CompleteParams{
			Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "p"},
			Argument: mcp.CompleteParamsArgument{Name: "a", Value: ""},
		},
	}
	got := s.completePromptArg(req)
	if got == nil {
		t.Fatal("completePromptArg returned nil")
	}
	if len(got.Completion.Values) != mcpCompletionLimit {
		t.Fatalf("values length = %d, want %d", len(got.Completion.Values), mcpCompletionLimit)
	}
	if !got.Completion.HasMore {
		t.Fatalf("HasMore = false; want true when truncated")
	}
	if got.Completion.Total != 150 {
		t.Fatalf("Total = %d, want 150", got.Completion.Total)
	}
}

// TestCompletionHandlerIntegration verifies the SDK-level completion
// handler installed at New-time dispatches to the static table.
func TestCompletionHandlerIntegration(t *testing.T) {
	s := New("t", "0.0.1")
	s.RegisterPromptArgCompletions("p", "arg", []string{"alpha", "beta"})

	// Simulate what mcp.NewServer would pass into the CompletionHandler
	// by invoking the one we installed through sdkOpts.
	if s.sdkOpts == nil || s.sdkOpts.CompletionHandler == nil {
		t.Fatal("expected completion handler installed on sdkOpts")
	}
	req := &mcp.CompleteRequest{Params: &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "p"},
		Argument: mcp.CompleteParamsArgument{Name: "arg", Value: "al"},
	}}
	res, err := s.sdkOpts.CompletionHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("CompletionHandler: %v", err)
	}
	want := []string{"alpha"}
	if !reflect.DeepEqual(res.Completion.Values, want) {
		t.Fatalf("values = %v, want %v", res.Completion.Values, want)
	}
}

// TestCompletionHandlerPreservesCallerHandler verifies a user-supplied
// CompletionHandler is preferred when it returns a non-empty result, and
// we fall through to the static table when it does not.
func TestCompletionHandlerPreservesCallerHandler(t *testing.T) {
	userCalls := 0
	user := func(_ context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
		userCalls++
		// Only respond for a specific prompt; otherwise return empty so
		// the static table can take over.
		if req.Params.Ref.Name == "dynamic" {
			return &mcp.CompleteResult{
				Completion: mcp.CompletionResultDetails{Values: []string{"from-user"}},
			}, nil
		}
		return nil, nil
	}
	s := New("t", "0.0.1", WithSDKOptions(&mcp.ServerOptions{CompletionHandler: user}))
	s.RegisterPromptArgCompletions("static", "arg", []string{"x", "y"})

	h := s.sdkOpts.CompletionHandler
	// Dynamic path, user handler wins.
	res, err := h(context.Background(), &mcp.CompleteRequest{Params: &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "dynamic"},
		Argument: mcp.CompleteParamsArgument{Name: "arg", Value: ""},
	}})
	if err != nil {
		t.Fatalf("h(dynamic): %v", err)
	}
	if !reflect.DeepEqual(res.Completion.Values, []string{"from-user"}) {
		t.Fatalf("dynamic: values = %v, want [from-user]", res.Completion.Values)
	}

	// Static path, user handler returns empty, static table takes over.
	res, err = h(context.Background(), &mcp.CompleteRequest{Params: &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "static"},
		Argument: mcp.CompleteParamsArgument{Name: "arg", Value: ""},
	}})
	if err != nil {
		t.Fatalf("h(static): %v", err)
	}
	if !reflect.DeepEqual(res.Completion.Values, []string{"x", "y"}) {
		t.Fatalf("static: values = %v, want [x y]", res.Completion.Values)
	}
	if userCalls != 2 {
		t.Fatalf("user handler calls = %d, want 2", userCalls)
	}
}
