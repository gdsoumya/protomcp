package protomcp

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestWithSDKOptions_LoggerWiredThrough verifies that a *slog.Logger
// passed via WithSDKOptions reaches the underlying mcp.Server and
// actually receives log entries. Spinning up a minimal MCP session and
// invoking initialize causes the SDK to emit at least one log line we
// can observe.
func TestWithSDKOptions_LoggerWiredThrough(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := New("logtest", "0.0.1",
		WithSDKOptions(&mcp.ServerOptions{Logger: log}),
	)

	// Handshake through an in-memory transport.
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	cT, sT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := s.SDK().Connect(ctx, sT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Drive a tools/list so the SDK has something to do.
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// We don't care about the exact text — we care that SOMETHING was
	// written. If the Logger were ignored, the buffer would be empty.
	if buf.Len() == 0 {
		t.Errorf("WithSDKOptions(Logger) did not propagate: buffer empty")
	}
}

// TestWithSDKOptions_InstructionsWiredThrough verifies that the
// Instructions field round-trips through initialize, proving the
// ServerOptions struct reaches the SDK's NewServer call.
func TestWithSDKOptions_InstructionsWiredThrough(t *testing.T) {
	want := "use the greeter politely"
	s := New("t", "0.0.1",
		WithSDKOptions(&mcp.ServerOptions{Instructions: want}),
	)

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	cT, sT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, _ := s.SDK().Connect(ctx, sT, nil)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// The Instructions field is returned on the initialize response and
	// stored on the client session.
	if got := cs.InitializeResult().Instructions; got != want {
		t.Errorf("Instructions = %q, want %q", got, want)
	}
}

// TestWithHTTPOptions_JSONResponse verifies that setting JSONResponse
// on StreamableHTTPOptions actually reaches the HTTP transport —
// JSONResponse mode returns application/json responses instead of the
// SSE stream that the default transport emits.
func TestWithHTTPOptions_JSONResponse(t *testing.T) {
	s := New("t", "0.0.1",
		WithHTTPOptions(&mcp.StreamableHTTPOptions{JSONResponse: true}),
	)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)

	// Fire a raw initialize request so we can inspect the response
	// content type.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"0.0.1"}}}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	ct := resp.Header.Get("Content-Type")
	// With JSONResponse = true we expect application/json (not
	// text/event-stream). The SDK may add a charset parameter.
	if want := "application/json"; ct != want && ct[:len(want)] != want {
		t.Errorf("Content-Type = %q, want prefix %q", ct, want)
	}
}

// TestOptionsOrderingAllowsPropagation is a regression test for the
// ordering bug we fixed: New used to construct mcp.NewServer BEFORE
// applying options, which silently ignored any WithSDKOptions. This
// test asserts that WithSDKOptions applied in any position of the
// variadic list still propagates to the SDK.
func TestOptionsOrderingAllowsPropagation(t *testing.T) {
	// Apply unrelated options on either side of WithSDKOptions to make
	// sure our post-apply construction holds regardless of position.
	s := New("t", "0.0.1",
		WithMiddleware(noopMiddleware()),
		WithSDKOptions(&mcp.ServerOptions{Instructions: "ordering works"}),
		WithMiddleware(noopMiddleware()),
	)

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	cT, sT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, _ := s.SDK().Connect(ctx, sT, nil)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	if got := cs.InitializeResult().Instructions; got != "ordering works" {
		t.Errorf("Instructions = %q (want 'ordering works'); WithSDKOptions did not propagate", got)
	}
}

func noopMiddleware() Middleware {
	return func(next Handler) Handler { return next }
}
