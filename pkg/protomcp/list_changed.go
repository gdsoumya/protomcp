package protomcp

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sentinelListChangedTemplate is added and removed to trigger the SDK's
// resource-list-changed notification; the scheme is clearly internal in
// case a client observes the sub-millisecond add/remove window.
const sentinelListChangedTemplate = "protomcp-internal-list-changed-trigger://{_}"

var errSentinelNotReadable = errors.New(
	"protomcp: internal list-changed-trigger template; not a readable resource")

// NotifyResourceListChanged emits notifications/resources/list_changed
// to every connected session. The SDK fires this only as a side effect
// of mutating its static registry, so this adds and immediately removes
// a sentinel template; the SDK's 10 ms debounce collapses both into one
// wire message. Safe to call concurrently and before any session
// connects.
func (s *Server) NotifyResourceListChanged() {
	s.sdk.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "_protomcp_list_changed_trigger",
		URITemplate: sentinelListChangedTemplate,
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return nil, errSentinelNotReadable
	})
	s.sdk.RemoveResourceTemplates(sentinelListChangedTemplate)
}

// RetryLoop runs fn with exponential backoff until ctx is canceled.
// Backoff starts at 100 ms, doubles per failure, caps at 30 s, with
// ±10% jitter. fn calls reset() after successful progress to revert
// backoff to the initial value. Any return from fn triggers a retry
// unless ctx is done.
func RetryLoop(ctx context.Context, fn func(ctx context.Context, reset func()) error) {
	const (
		initialBackoff = 100 * time.Millisecond
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff
	reset := func() { backoff = initialBackoff }

	for {
		if ctx.Err() != nil {
			return
		}
		_ = fn(ctx, reset)
		if ctx.Err() != nil {
			return
		}
		jitter := time.Duration(rand.Int64N(int64(backoff / 5))) //nolint:gosec // non-cryptographic jitter only
		sleep := backoff - backoff/10 + jitter
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}
