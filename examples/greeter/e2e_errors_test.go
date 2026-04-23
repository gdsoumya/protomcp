package greeter_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// buildGreeterServer wires the Greeter gRPC service behind an in-memory
// MCP session so the tests below can drive tool calls through the full
// generated handler → middleware chain → gRPC client → gRPC server path.
func buildGreeterServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	grpcClient := startGRPC(t)
	srv := protomcp.New("greeter", "0.1.0")
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)
	return connect(context.Background(), t, srv, nil)
}

// TestGRPCNotFoundRoundTrip verifies that a gRPC NotFound from the
// upstream service surfaces as a successful CallToolResult with
// IsError=true and TextContent containing the code + message, matching
// DefaultErrorHandler's contract.
func TestGRPCNotFoundRoundTrip(t *testing.T) {
	cs := buildGreeterServer(t)
	ctx := context.Background()

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "Greeter_FailWith",
		Arguments: json.RawMessage(
			`{"code":` + itoa(int(codes.NotFound)) + `,"message":"widget 42 not found"}`,
		),
	})
	if err != nil {
		t.Fatalf("CallTool returned JSON-RPC error, want CallToolResult: %v", err)
	}
	if !out.IsError {
		t.Fatalf("IsError=false, want true; out=%+v", out)
	}
	if len(out.Content) == 0 {
		t.Fatalf("no content: %+v", out)
	}
	tc, ok := out.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", out.Content[0])
	}
	if !strings.Contains(tc.Text, "NotFound") {
		t.Errorf("text %q missing code NotFound", tc.Text)
	}
	if !strings.Contains(tc.Text, "widget 42 not found") {
		t.Errorf("text %q missing server message", tc.Text)
	}
}

// TestGRPCUnauthenticatedRoundTrip verifies that a gRPC Unauthenticated
// from upstream escalates through DefaultErrorHandler into a JSON-RPC
// error, the SDK's CallTool returns a non-nil error and no result.
func TestGRPCUnauthenticatedRoundTrip(t *testing.T) {
	cs := buildGreeterServer(t)
	ctx := context.Background()

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "Greeter_FailWith",
		Arguments: json.RawMessage(
			`{"code":` + itoa(int(codes.Unauthenticated)) + `,"message":"bad token"}`,
		),
	})
	if err == nil {
		t.Fatalf("err = nil, want non-nil JSON-RPC error; out=%+v", out)
	}
	// The SDK surfaces JSON-RPC errors by returning a non-nil error.
	// The message propagated from the server should mention the reason.
	if !strings.Contains(err.Error(), "bad token") && !strings.Contains(err.Error(), "Unauthenticated") {
		t.Errorf("err = %q, want to mention reason or code", err.Error())
	}
}

// TestGRPCInvalidArgumentRoundTrip verifies that a gRPC InvalidArgument
// is folded into IsError=true with TextContent carrying the "Code:
// Message" form and, where the SDK does not clobber it with schema
// defaults, a StructuredContent carrying the google.rpc.Status proto
// serialized as JSON.
//
// Note: the SDK's generic mcp.AddTool wrapper overwrites any
// StructuredContent the handler populated with a fresh value derived
// from the tool's Out return (applying output-schema defaults). Since
// the generated handler returns Out=nil json.RawMessage on the error
// path, the wire-level StructuredContent ends up reshaped to match the
// success-path output schema (e.g. an empty HelloReply {}). The Status
// proto therefore survives in TextContent but not reliably in
// StructuredContent on the error path. This test asserts the guaranteed
// shape (IsError + TextContent) and tolerates either StructuredContent
// outcome.
func TestGRPCInvalidArgumentRoundTrip(t *testing.T) {
	cs := buildGreeterServer(t)
	ctx := context.Background()

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "Greeter_FailWith",
		Arguments: json.RawMessage(
			`{"code":` + itoa(int(codes.InvalidArgument)) + `,"message":"name must be ascii"}`,
		),
	})
	if err != nil {
		t.Fatalf("CallTool returned JSON-RPC error, want CallToolResult: %v", err)
	}
	if !out.IsError {
		t.Fatalf("IsError=false, want true; out=%+v", out)
	}
	if len(out.Content) == 0 {
		t.Fatalf("no content: %+v", out)
	}
	tc, ok := out.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", out.Content[0])
	}
	if !strings.Contains(tc.Text, "InvalidArgument") {
		t.Errorf("text %q missing code InvalidArgument", tc.Text)
	}
	if !strings.Contains(tc.Text, "name must be ascii") {
		t.Errorf("text %q missing server message", tc.Text)
	}
}

// TestGRPCInvalidArgumentStructuredStatusProto directly exercises
// DefaultErrorHandler (not the full SDK round-trip) to verify the
// handler emits a google.rpc.Status proto in StructuredContent for
// non-auth gRPC codes. This is the contract the error mapper owes
// callers who invoke it directly (or write their own tool handler that
// does not go through mcp.AddTool's Out-return clobbering).
func TestGRPCInvalidArgumentStructuredStatusProto(t *testing.T) {
	err := grpcstatus.Error(codes.InvalidArgument, "name must be ascii")
	srv := protomcp.New("t", "0.0.1")
	res, herr := srv.HandleToolError(context.Background(), &mcp.CallToolRequest{}, err)
	if herr != nil {
		t.Fatalf("herr = %v, want nil", herr)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want json.RawMessage", res.StructuredContent)
	}
	var decoded struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("StructuredContent not JSON: %v (%s)", err, raw)
	}
	if decoded.Code != int(codes.InvalidArgument) {
		t.Errorf("Status.code = %d, want %d", decoded.Code, int(codes.InvalidArgument))
	}
	if decoded.Message != "name must be ascii" {
		t.Errorf("Status.message = %q, want %q", decoded.Message, "name must be ascii")
	}
}

// TestComplexRoundTrip verifies protojson round-trips through MCP
// preserve nested messages, repeated fields, enums, and map fields.
// Exercises EchoComplex end-to-end through the generated handler.
func TestComplexRoundTrip(t *testing.T) {
	cs := buildGreeterServer(t)
	ctx := context.Background()

	// Build a request with every shape: nested message, repeated scalar,
	// enum (as string name), map<string,int32>. Pass the map directly as
	// Arguments (typed `any`), the SDK encodes it once; wrapping in
	// json.RawMessage forces double-encoding which the server rejects.
	args := map[string]any{
		"name": "alice",
		"tags": []string{"admin", "beta"},
		"mood": "MOOD_EXCITED",
		"address": map[string]any{
			"street": "1 Infinite Loop",
			"city":   "Cupertino",
			"zip":    "95014",
		},
		"counters": map[string]int32{
			"red":   1,
			"green": 2,
			"blue":  3,
		},
	}

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Greeter_EchoComplex",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out.IsError {
		tc, _ := out.Content[0].(*mcp.TextContent)
		if tc != nil {
			t.Fatalf("unexpected IsError: %s", tc.Text)
		}
		t.Fatalf("unexpected IsError: %+v", out)
	}
	text := out.Content[0].(*mcp.TextContent).Text

	// Decode the echo and confirm every shape survived round-trip.
	var got struct {
		Name     string            `json:"name"`
		Tags     []string          `json:"tags"`
		Mood     string            `json:"mood"`
		Address  map[string]string `json:"address"`
		Counters map[string]int32  `json:"counters"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal echo: %v (%s)", err, text)
	}
	if got.Name != "alice" {
		t.Errorf("name = %q, want alice", got.Name)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "admin" || got.Tags[1] != "beta" {
		t.Errorf("tags = %v, want [admin beta]", got.Tags)
	}
	if got.Mood != "MOOD_EXCITED" {
		t.Errorf("mood = %q, want MOOD_EXCITED", got.Mood)
	}
	if got.Address["street"] != "1 Infinite Loop" ||
		got.Address["city"] != "Cupertino" ||
		got.Address["zip"] != "95014" {
		t.Errorf("address = %+v, want Cupertino loop", got.Address)
	}
	wantCounters := map[string]int32{"red": 1, "green": 2, "blue": 3}
	for k, v := range wantCounters {
		if got.Counters[k] != v {
			t.Errorf("counters[%s] = %d, want %d", k, got.Counters[k], v)
		}
	}
}

// TestContextCancellationPropagation exercises the Slow RPC to verify
// that caller-side context cancellation reaches the upstream gRPC
// server. The server's Slow handler blocks on ctx.Done() and returns
// the underlying cancel reason as a gRPC status; we only require that
// the call terminates rather than hangs. The exact error shape depends
// on whether the SDK's in-memory transport threads ctx cancel through
// to the tool handler, if it does, we observe DeadlineExceeded mapped
// to a JSON-RPC error; if it doesn't, the call may still return a
// context error from the client side. Either outcome is acceptable; a
// hang is not.
func TestContextCancellationPropagation(t *testing.T) {
	cs := buildGreeterServer(t)

	// Bound the test with a generous safety timer, if the SDK's
	// in-memory transport does not propagate cancel, we want the
	// callerCtx deadline to fire and the test to fail fast rather than
	// time out at the `go test` level.
	callerCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(callerCtx, &mcp.CallToolParams{
			Name:      "Greeter_Slow",
			Arguments: json.RawMessage(`{"name":"world"}`),
		})
		done <- err
	}()

	select {
	case err := <-done:
		// Any outcome is fine except an unrelated successful
		// completion of the handler (Slow blocks forever unless
		// cancel reaches it). A non-nil error here means cancel did
		// propagate at some layer.
		if err == nil {
			t.Fatalf("Slow returned nil err; cancel did not propagate at any layer")
		}
		t.Logf("Slow call terminated with err: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("Slow did not terminate after context deadline expired, cancel did not propagate through any layer")
	}
}

// itoa avoids pulling strconv into this file just to format a single
// enum value in the test fixtures above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
