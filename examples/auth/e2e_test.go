// Package auth_test is the end-to-end acceptance test for the core protomcp
// use case: a stdlib HTTP middleware authenticates the caller and stashes the
// resolved principal on the request context; a protomcp.Middleware reads the
// principal and writes x-user-id / x-tenant into the outgoing gRPC metadata;
// the upstream gRPC server's interceptor reads those keys out. Zero code
// changes to the user's gRPC service.
//
// Two concerns, two layers, same pattern as grpc-gateway.
package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	authv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/auth/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	authserver "github.com/gdsoumya/protomcp/examples/auth/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type principal struct{ UserID, Tenant string }

type principalKey struct{}

// httpAuthMiddleware is an ordinary stdlib http.Handler middleware. It
// validates the bearer token, rejects unauthenticated requests with 401,
// and stashes the resolved principal on the request context for downstream
// consumers.
func httpAuthMiddleware(valid map[string]principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := valid[r.Header.Get("Authorization")]
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// principalToMetadata is a protomcp.Middleware. It reads the principal
// that httpAuthMiddleware stashed on ctx and copies it into the outgoing
// gRPC metadata so the upstream server can read it.
func principalToMetadata() protomcp.Middleware {
	return func(next protomcp.Handler) protomcp.Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
			p, ok := ctx.Value(principalKey{}).(principal)
			if !ok {
				return nil, fmt.Errorf("principal missing on ctx (httpAuthMiddleware bug?)")
			}
			g.Metadata.Set("x-user-id", p.UserID)
			g.Metadata.Set("x-tenant", p.Tenant)
			return next(ctx, req, g)
		}
	}
}

// startTestGRPCServer boots the Profile gRPC service with its auth
// interceptor on a random local port and returns a client.
func startTestGRPCServer(t *testing.T) authv1.ProfileClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(authserver.PrincipalInterceptor()))
	authv1.RegisterProfileServer(grpcSrv, authserver.New())
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
	return authv1.NewProfileClient(conn)
}

// TestHTTPtoGRPCMetadataPropagation is the real end-to-end acceptance test.
// It spins up an httptest.Server with httpAuthMiddleware wrapped around the
// protomcp.Server (which is itself an http.Handler — the grpc-gateway
// pattern), then runs an MCP client against it over the wire.
func TestHTTPtoGRPCMetadataPropagation(t *testing.T) {
	grpcClient := startTestGRPCServer(t)

	valid := map[string]principal{
		"Bearer alice-token": {UserID: "alice", Tenant: "acme"},
		"Bearer bob-token":   {UserID: "bob", Tenant: "globex"},
	}

	// Build the MCP server with ONLY the metadata-propagation middleware —
	// HTTP-layer auth lives outside, as stdlib middleware.
	srv := protomcp.New("auth-example", "0.1.0",
		protomcp.WithMiddleware(principalToMetadata()),
	)
	authv1.RegisterProfileMCPTools(srv, grpcClient)

	httpSrv := httptest.NewServer(httpAuthMiddleware(valid)(srv))
	t.Cleanup(httpSrv.Close)

	cases := map[string]struct {
		token      string
		wantUser   string
		wantTenant string
		wantHTTP   int // 0 = success
	}{
		"alice":   {token: "Bearer alice-token", wantUser: "alice", wantTenant: "acme"},
		"bob":     {token: "Bearer bob-token", wantUser: "bob", wantTenant: "globex"},
		"invalid": {token: "Bearer mallory", wantHTTP: http.StatusUnauthorized},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			// Unauthenticated clients can't even establish the MCP session —
			// the HTTP middleware rejects with 401 before the SDK sees them.
			if tc.wantHTTP != 0 {
				assertHTTP401(t, httpSrv.URL, tc.token)
				return
			}

			cs := connectHTTPClient(ctx, t, httpSrv.URL, tc.token)
			out, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name:      "Profile_WhoAmI",
				Arguments: map[string]any{},
			})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if out.IsError {
				t.Fatalf("unexpected IsError: %+v", out)
			}
			var resp struct {
				UserID string `json:"userId"`
				Tenant string `json:"tenant"`
			}
			text := out.Content[0].(*mcp.TextContent).Text
			if err := json.Unmarshal([]byte(text), &resp); err != nil {
				t.Fatalf("unmarshal: %v (%s)", err, text)
			}
			if resp.UserID != tc.wantUser || resp.Tenant != tc.wantTenant {
				t.Errorf("got user=%q tenant=%q; want user=%q tenant=%q",
					resp.UserID, resp.Tenant, tc.wantUser, tc.wantTenant)
			}
		})
	}
}

// assertHTTP401 verifies the MCP endpoint rejects an unauthenticated
// request at the HTTP layer (proper 401 response, not a JSON-RPC error).
func assertHTTP401(t *testing.T, url, token string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	req.Header.Set("Authorization", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// connectHTTPClient opens an MCP client over the real streamable-HTTP
// transport, attaching the Authorization header to every request so the
// HTTP middleware admits it.
func connectHTTPClient(ctx context.Context, t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &headerAdderRT{
				base:   http.DefaultTransport,
				header: "Authorization",
				value:  token,
			},
		},
	}
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type headerAdderRT struct {
	base          http.RoundTripper
	header, value string
}

func (r *headerAdderRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(r.header, r.value)
	return r.base.RoundTrip(req)
}
