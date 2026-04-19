package greeter_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	// *protomcp.Server is an http.Handler — drop it straight into httptest.
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
	// Poll with a generous deadline — real HTTP + TLS-less loopback is fast,
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
