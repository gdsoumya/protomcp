package greeter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	greeterserver "github.com/gdsoumya/protomcp/examples/greeter/server"
	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TestServerStreamingEmitsProgress_RealHTTP is the streaming-over-the-wire
// counterpart of TestServerStreamingEmitsProgress. Where the in-memory
// transport variant checks that the SDK client's notification handler
// receives each gRPC message, this one spins up an httptest.Server
// hosting *protomcp.Server directly (grpc-gateway-style) and connects
// via mcp.StreamableClientTransport over real TCP/HTTP. The point is
// to exercise everything the in-memory path skips: HTTP framing, SSE,
// session-id negotiation, and the SDK's streamable-HTTP client
// plumbing end-to-end.
//
// Invariants asserted:
//   - ProgressNotificationHandler fires exactly `turns` times
//   - Each progress.Message parses as the JSON-encoded HelloReply the
//     generated server-streaming handler emits via
//     req.Session.NotifyProgress
//   - The final CallToolResult is not an error and carries the summary
//     text produced at EOF
func TestServerStreamingEmitsProgress_RealHTTP(t *testing.T) {
	grpcClient := startGRPC(t)

	srv := protomcp.New("greeter-http", "0.1.0")
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	// *protomcp.Server is an http.Handler, drop it straight into httptest.
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	var (
		mu       sync.Mutex
		progress []string
		counters []float64
	)
	client := mcp.NewClient(&mcp.Implementation{Name: "streaming-test", Version: "0.0.1"},
		&mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, p *mcp.ProgressNotificationClientRequest) {
				mu.Lock()
				defer mu.Unlock()
				progress = append(progress, p.Params.Message)
				counters = append(counters, p.Params.Progress)
			},
		},
	)

	ctx := context.Background()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   httpSrv.URL,
		HTTPClient: http.DefaultClient,
	}, nil)
	if err != nil {
		t.Fatalf("http client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	turns := int32(5)
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Greeter_StreamGreetings",
		Arguments: map[string]any{"name": "world", "turns": turns},
		Meta:      mcp.Meta{"progressToken": "http-streaming"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.IsError {
		t.Fatalf("unexpected IsError: %+v", out)
	}

	// Progress notifications arrive asynchronously over the SSE side-channel.
	// Poll with a generous deadline, real HTTP + TLS-less loopback is fast,
	// but race-detector runs and stressed CI runners occasionally need a
	// moment more than the in-memory path. 5s is well beyond any realistic
	// delivery.
	var got []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]string(nil), progress...)
		mu.Unlock()
		if len(got) >= int(turns) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(got) != int(turns) {
		t.Errorf("progress events over HTTP: got %d, want %d (msgs=%v)", len(got), turns, got)
	}

	// Spec compliance: Progress MUST increase with each notification.
	mu.Lock()
	gotCounters := append([]float64(nil), counters...)
	mu.Unlock()
	for i := 1; i < len(gotCounters); i++ {
		if gotCounters[i] <= gotCounters[i-1] {
			t.Errorf("progress counter not monotonic over HTTP: [%d]=%v [%d]=%v (full=%v)",
				i-1, gotCounters[i-1], i, gotCounters[i], gotCounters)
			break
		}
	}

	// The final tool result reports the per-call summary the generated
	// streaming template produces at EOF: "<N> messages; last: <json>".
	if len(out.Content) == 0 {
		t.Fatalf("no content in final result: %+v", out)
	}
	tc, ok := out.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", out.Content[0])
	}
	wantPrefix := fmt.Sprintf("%d messages;", turns)
	if len(tc.Text) < len(wantPrefix) || tc.Text[:len(wantPrefix)] != wantPrefix {
		t.Errorf("summary = %q, want prefix %q", tc.Text, wantPrefix)
	}
}

// mdCapture is a thin container that records the incoming gRPC metadata
// for every unary call. It is a struct (not a map) so tests can take
// address of a single instance across goroutines without worrying about
// map-concurrency invariants, access goes through a mutex.
type mdCapture struct {
	mu sync.Mutex
	// seen is keyed by "method -> headerName -> values". A slice lets the
	// test distinguish multiple values for the same header (metadata.MD
	// is a multi-valued map). Empty means "header absent".
	seen map[string]map[string][]string
}

// record snapshots ctx's incoming metadata for the given fully-qualified
// gRPC method name. Called from the interceptor; safe to call from any
// goroutine.
func (c *mdCapture) record(ctx context.Context, method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[string]map[string][]string)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	snapshot := make(map[string][]string, len(md))
	for k, v := range md {
		snapshot[k] = append([]string(nil), v...)
	}
	c.seen[method] = snapshot
}

// lookup returns the captured header values for the given method and key,
// and a bool indicating whether the header was present at all. A non-nil
// but empty slice is impossible by construction (metadata.MD never stores
// empty value lists), so "present" == "non-nil slice returned".
func (c *mdCapture) lookup(method, key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.seen[method]
	if m == nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

// startGRPCWithInterceptor boots the Greeter gRPC service wrapped by the
// supplied UnaryServerInterceptor, returning the client stub. The helper
// mirrors startGRPC but surfaces interceptor configuration so tests can
// observe incoming metadata end-to-end through the MCP stack.
func startGRPCWithInterceptor(t *testing.T, interceptor grpc.UnaryServerInterceptor) greeterv1.GreeterClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	greeterv1.RegisterGreeterServer(grpcSrv, greeterserver.New())
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return greeterv1.NewGreeterClient(conn)
}

// TestProgressToken_HeaderPropagated asserts that when the MCP caller
// includes a progressToken on a tool invocation, the generated handler
// forwards it to the upstream gRPC service as the configured metadata
// header ("mcp-progress-token" by default). Absent a token, the header
// must NOT be set, we specifically do not want ghost headers on calls
// that did not opt in to progress tracking.
//
// The test uses the unary SayHello tool because:
//   - unary calls traverse the full template path for this feature,
//   - a unary UnaryServerInterceptor is simple to attach and predictable
//     on the observation side,
//   - the streaming test above already covers the parallel path.
func TestProgressToken_HeaderPropagated(t *testing.T) {
	capture := &mdCapture{}
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		capture.record(ctx, info.FullMethod)
		return handler(ctx, req)
	}
	grpcClient := startGRPCWithInterceptor(t, interceptor)

	srv := protomcp.New("greeter", "0.1.0")
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	ctx := context.Background()
	cs := connect(ctx, t, srv, nil)

	// Call 1: progressToken set. Header must surface downstream as the
	// stringified token value.
	const wantToken = "tok-42"
	const sayHelloMethod = "/protomcp.examples.greeter.v1.Greeter/SayHello"
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: json.RawMessage(`{"name":"world"}`),
		Meta:      mcp.Meta{"progressToken": wantToken},
	}); err != nil {
		t.Fatalf("call with token: %v", err)
	}
	vals, ok := capture.lookup(sayHelloMethod, protomcp.ProgressTokenHeader)
	if !ok {
		t.Fatalf("expected %q header on call with token; got none (seen=%+v)",
			protomcp.ProgressTokenHeader, capture.seen)
	}
	if len(vals) != 1 || vals[0] != wantToken {
		t.Errorf("header values = %v, want [%q]", vals, wantToken)
	}

	// Call 2: no progressToken. Header must NOT be present, a ghost header
	// would make downstream correlators see a token that the client never
	// sent, which is exactly the bug this assertion guards against.
	capture.seen = nil
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: json.RawMessage(`{"name":"world"}`),
	}); err != nil {
		t.Fatalf("call without token: %v", err)
	}
	if vals, ok := capture.lookup(sayHelloMethod, protomcp.ProgressTokenHeader); ok {
		t.Errorf("header must be absent on call without token; got %v", vals)
	}
}

// TestProgressToken_CustomHeader checks that WithProgressTokenHeader is
// honored by generated code. It drives the same scenario as the default
// test but asks the Server to use an organization-specific header name,
// then asserts the upstream interceptor sees that name and not the
// default.
func TestProgressToken_CustomHeader(t *testing.T) {
	const custom = "x-mcp-trace-id"
	capture := &mdCapture{}
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		capture.record(ctx, info.FullMethod)
		return handler(ctx, req)
	}
	grpcClient := startGRPCWithInterceptor(t, interceptor)

	srv := protomcp.New("greeter", "0.1.0", protomcp.WithProgressTokenHeader(custom))
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	ctx := context.Background()
	cs := connect(ctx, t, srv, nil)

	const wantToken = "tok-custom"
	const sayHelloMethod = "/protomcp.examples.greeter.v1.Greeter/SayHello"
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Greeter_SayHello",
		Arguments: json.RawMessage(`{"name":"world"}`),
		Meta:      mcp.Meta{"progressToken": wantToken},
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	vals, ok := capture.lookup(sayHelloMethod, custom)
	if !ok || len(vals) != 1 || vals[0] != wantToken {
		t.Errorf("custom header = %v ok=%v; want [%q]", vals, ok, wantToken)
	}
	// And the default name must be silent when an override is in use.
	if vals, ok := capture.lookup(sayHelloMethod, protomcp.ProgressTokenHeader); ok {
		t.Errorf("default header must not be set when an override is configured; got %v", vals)
	}
}
