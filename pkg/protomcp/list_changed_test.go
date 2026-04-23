package protomcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestNotifyResourceListChanged_NoPanic, the helper must be safe to
// call before any session has connected and safe to call repeatedly.
// Wire-level observation of `notifications/resources/list_changed`
// lives in examples/tasks/e2e_resources_test.go (needs a full MCP
// session to catch the notification); here we just exercise the
// local code path.
func TestNotifyResourceListChanged_NoPanic(t *testing.T) {
	s := New("t", "0.0.1")
	// Sequential calls, the sentinel must be cleanly added+removed
	// each time so subsequent calls don't trip "template already exists".
	for range 3 {
		s.NotifyResourceListChanged()
	}
}

// TestRetryLoop_ContextCancel, fn runs until ctx is canceled; the
// loop exits without spinning, and the final return from fn is
// observed by the caller.
func TestRetryLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32

	done := make(chan struct{})
	go func() {
		RetryLoop(ctx, func(ctx context.Context, _ func()) error {
			calls.Add(1)
			return errors.New("fail") // always fails; loop should retry
		})
		close(done)
	}()

	// Give it a few retry cycles.
	time.Sleep(250 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RetryLoop did not exit within 1s of ctx cancel")
	}
	if n := calls.Load(); n < 1 {
		t.Errorf("fn was never called (calls=%d)", n)
	}
}

// TestRetryLoop_ResetRevertsBackoff, after reset() is called,
// subsequent retries happen at the initial backoff again (observable
// as "many iterations per second" once reset has fired, versus "few"
// if backoff kept growing). We measure by counting iterations over
// a fixed window after forcing a reset on each call.
func TestRetryLoop_ResetRevertsBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	RetryLoop(ctx, func(_ context.Context, reset func()) error {
		calls.Add(1)
		reset()
		return errors.New("fail")
	})
	// With reset fired on every iteration, backoff stays at ~100ms
	// (±10% jitter). Over 700ms we expect >= 5 iterations; without
	// reset, iterations would be 1,2,3 (backoffs 100, 200, 400) → 4
	// iterations max in the same window.
	if n := calls.Load(); n < 5 {
		t.Errorf("calls = %d over 700ms with reset; want ≥5 (backoff stuck above 100ms?)", n)
	}
}

// TestRetryLoop_ContextRespectedMidSleep, if ctx cancels while the
// loop is sleeping between retries, wake up immediately.
func TestRetryLoop_ContextRespectedMidSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	done := make(chan struct{})
	go func() {
		RetryLoop(ctx, func(_ context.Context, _ func()) error {
			// Force backoff to grow: return immediately, don't call reset.
			// After two iterations backoff is ~400ms; we'll cancel before
			// the sleep completes.
			return errors.New("fail")
		})
		close(done)
	}()

	// Let two iterations tick, then cancel mid-sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("RetryLoop did not wake up on ctx cancel (elapsed=%v)", time.Since(start))
	}
}
