package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/auth/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestAuthGRPCUnauthenticatedRoundTrip verifies that when the upstream
// PrincipalInterceptor rejects the call with codes.Unauthenticated ,
// because no middleware wrote x-user-id into the outgoing metadata ,
// the MCP client observes a JSON-RPC protocol error (non-nil err from
// CallTool) rather than an IsError result. Auth codes short-circuit to
// the JSON-RPC layer by design; see pkg/protomcp/errors.go.
func TestAuthGRPCUnauthenticatedRoundTrip(t *testing.T) {
	grpcClient := startTestGRPCServer(t)

	// Intentionally no middleware, the upstream interceptor sees missing
	// x-user-id and rejects with Unauthenticated.
	srv := protomcp.New("auth-example", "0.1.0")
	authv1.RegisterProfileMCPTools(srv, grpcClient)

	// Mount the Server directly as an http.Handler (no outer HTTP auth,
	// so requests reach the tool handler freely).
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	cs := connectHTTPClient(context.Background(), t, httpSrv.URL, "")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "Profile_WhoAmI",
		Arguments: map[string]any{},
	})
	if err == nil {
		t.Fatalf("err = nil, want non-nil JSON-RPC error")
	}
	if !strings.Contains(err.Error(), "Unauthenticated") && !strings.Contains(err.Error(), "missing x-user-id") {
		t.Errorf("err = %q, want to mention Unauthenticated or missing x-user-id", err.Error())
	}
}

// TestHTTPAuthRejectsBefore MCP verifies that the HTTP-layer
// authentication middleware rejects unauthenticated requests with a
// proper 401, before the MCP SDK even sees the request. This is the
// behavior that the old HeadersFromContext-based design couldn't
// express cleanly: auth failures are a transport-level concern.
func TestHTTPAuthRejectsBeforeMCP(t *testing.T) {
	grpcClient := startTestGRPCServer(t)
	srv := protomcp.New("auth-example", "0.1.0",
		protomcp.WithToolMiddleware(principalToMetadata()),
	)
	authv1.RegisterProfileMCPTools(srv, grpcClient)

	httpSrv := httptest.NewServer(
		httpAuthMiddleware(map[string]principal{
			"Bearer good": {UserID: "u", Tenant: "t"},
		})(srv),
	)
	t.Cleanup(httpSrv.Close)

	// Unauthenticated POSTs get HTTP 401 from the outer middleware.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, httpSrv.URL, nil)
	req.Header.Set("Authorization", "Bearer mallory")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
