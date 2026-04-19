package protomcp

import (
	"strings"
	"testing"
)

// TestWithMiddleware_NilPanics verifies the safety net: a nil
// Middleware registration is caught at New-time with a clear panic
// message instead of deferred to the first tool call.
func TestWithMiddleware_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for WithMiddleware(nil)")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithMiddleware received nil") {
			t.Errorf("panic message = %q, want to mention WithMiddleware", msg)
		}
	}()
	_ = New("t", "0.0.1", WithMiddleware(nil))
}

// TestWithResultProcessor_NilPanics is the processor-side analog.
func TestWithResultProcessor_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for WithResultProcessor(nil)")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithResultProcessor received nil") {
			t.Errorf("panic message = %q, want to mention WithResultProcessor", msg)
		}
	}()
	_ = New("t", "0.0.1", WithResultProcessor(nil))
}
