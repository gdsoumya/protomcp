// This file demonstrates a realistic pattern: gating
// `resources/subscribe` on a per-principal ACL so a caller cannot
// subscribe to a URI they should not see.
//
// The setup:
//   - An HTTP middleware extracts a bearer token from each request and
//     stashes the resolved *principal on the request ctx. (For a
//     production auth layer, use the MCP Go SDK's
//     auth.RequireBearerToken; see examples/auth/cmd/sdkauth.)
//   - An authorize() wrapper composes over the Manager's Subscribe to
//     enforce URI-access rules before the subscription is recorded.
//
// Unsubscribe is intentionally not re-checked: a caller can always
// release a subscription they previously held, and denying unsubscribe
// when access has been revoked would just leak server state.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// principal is the per-session identity the authz layer reads from
// ctx. A real deployment swaps this for a JWT claim set, a TokenInfo
// from the MCP Go SDK's auth.RequireBearerToken, or whatever your IdP
// emits.
type principal struct {
	UserID string
	// AllowedURIPrefixes restricts what resource URIs this principal
	// may subscribe to. A principal whose list contains "tasks://" can
	// subscribe to any task; one with "tasks://1" only to task 1.
	AllowedURIPrefixes []string
}

// bearerTokens is the demo ACL. In production this would be a token
// verifier backed by an IdP.
var bearerTokens = map[string]*principal{
	"alice-token": {UserID: "alice", AllowedURIPrefixes: []string{"tasks://"}},
	"bob-token":   {UserID: "bob", AllowedURIPrefixes: []string{"tasks://1"}},
}

type principalKey struct{}

// authMiddleware resolves the Authorization bearer into a *principal
// and stashes it on the request ctx. Downstream the MCP
// SubscribeHandler reads it via principalFromContext.
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(hdr, "Bearer ")
		if tok == "" || tok == hdr {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		p, ok := bearerTokens[tok]
		if !ok {
			http.Error(w, "unknown bearer token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// principalFromContext returns the principal resolved by authMiddleware,
// or nil if none is present.
func principalFromContext(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey{}).(*principal)
	return p
}

// subscribeHandler matches the MCP Go SDK's ServerOptions.SubscribeHandler
// shape.
type subscribeHandler = func(context.Context, *mcp.SubscribeRequest) error

// authorize wraps a SubscribeHandler with an ACL check. A caller with
// no principal on ctx is rejected as unauthenticated; a caller whose
// principal's AllowedURIPrefixes do not cover the requested URI is
// rejected as forbidden. On allow, delegation passes through to the
// wrapped handler (which records the subscription in the SDK's
// session-tracking map).
//
// The returned error surfaces as a JSON-RPC error on the subscribe
// call; the SDK does not record the session for rejected subscribes.
func authorize(next subscribeHandler) subscribeHandler {
	return func(ctx context.Context, req *mcp.SubscribeRequest) error {
		p := principalFromContext(ctx)
		if p == nil {
			return fmt.Errorf("subscribe: unauthenticated")
		}
		uri := req.Params.URI
		for _, prefix := range p.AllowedURIPrefixes {
			if strings.HasPrefix(uri, prefix) {
				return next(ctx, req)
			}
		}
		return fmt.Errorf("subscribe: principal %q not permitted to subscribe to %q", p.UserID, uri)
	}
}
