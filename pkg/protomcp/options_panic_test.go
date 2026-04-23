package protomcp

import (
	"strings"
	"testing"
)

// TestWithMiddleware_NilPanics verifies the safety net: a nil
// ToolMiddleware registration is caught at New-time with a clear panic
// message instead of deferred to the first tool call.
func TestWithMiddleware_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for WithToolMiddleware(nil)")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithToolMiddleware received nil") {
			t.Errorf("panic message = %q, want to mention WithToolMiddleware", msg)
		}
	}()
	_ = New("t", "0.0.1", WithToolMiddleware(nil))
}

// TestWithResultProcessor_NilPanics is the processor-side analog.
func TestWithResultProcessor_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for WithToolResultProcessor(nil)")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithToolResultProcessor received nil") {
			t.Errorf("panic message = %q, want to mention WithToolResultProcessor", msg)
		}
	}()
	_ = New("t", "0.0.1", WithToolResultProcessor(nil))
}
