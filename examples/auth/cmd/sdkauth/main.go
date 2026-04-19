// Command sdkauth demonstrates the same two-layer auth pattern as the
// sibling `auth` example, but using the MCP Go SDK's native
// auth.RequireBearerToken middleware instead of a hand-rolled stdlib
// HTTP middleware.
//
//   - Layer 1: auth.RequireBearerToken wraps the protomcp.Server. The
//     verifier extracts the principal and the SDK stashes the resulting
//     *auth.TokenInfo on ctx via auth.TokenInfoFromContext.
//   - Layer 2: a protomcp.Middleware reads TokenInfoFromContext and
//     writes x-user-id / x-tenant into the outgoing gRPC metadata.
//
// The upstream PrincipalInterceptor reads those keys exactly as in the
// `auth` example — zero gRPC-side changes. The SDK path is the right
// choice when you want OAuth 2.1 spec alignment (WWW-Authenticate
// header, RFC 9728 Protected Resource Metadata discovery, scope
// enforcement) out of the box.
//
// Try it:
//
//	go run ./examples/auth/cmd/sdkauth &
//	curl -sN -H 'Authorization: Bearer alice-token' \
//	     -H 'Content-Type: application/json' \
//	     -H 'Accept: application/json, text/event-stream' \
//	     -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
//	          "params":{"protocolVersion":"2025-06-18","capabilities":{},
//	                    "clientInfo":{"name":"c","version":"0"}}}' \
//	     http://localhost:8080/
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	authserver "github.com/gdsoumya/protomcp/examples/auth/server"
	authv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/auth/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// validTokens is the hardcoded bearer-token table this demo uses in
// lieu of a real identity provider. Each entry is keyed by the bearer
// value (sans the "Bearer " prefix — the SDK strips that for us).
var validTokens = map[string]struct {
	UserID string
	Tenant string
}{
	"alice-token": {UserID: "alice", Tenant: "acme"},
	"bob-token":   {UserID: "bob", Tenant: "globex"},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server (loopback by default; set 0.0.0.0:PORT for all interfaces)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr)
	stop()
	if err != nil {
		log.Fatalf("sdkauth: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	grpcClient, shutdownGRPC, err := startAuthGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// Layer 2: protomcp.Middleware lifts the SDK's TokenInfo off ctx
	// and copies the relevant fields into outgoing gRPC metadata.
	srv := protomcp.New("sdkauth-mcp", "0.1.0",
		protomcp.WithMiddleware(tokenInfoToMetadata()),
	)
	authv1.RegisterProfileMCPTools(srv, grpcClient)

	// Layer 1: the SDK's own bearer-token middleware. It parses the
	// Authorization header, hands the raw token to our verifier, and on
	// success puts the resulting *auth.TokenInfo on the request ctx.
	// On failure it responds with HTTP 401 + a WWW-Authenticate header
	// (RFC 6750 / 9728 compliant) before our server ever sees the call.
	bearer := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		// Scopes left empty here; set this to require specific scopes.
	})

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           bearer(srv),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("sdkauth-mcp listening on %s\n", addr)
	fmt.Println("try: curl -H 'Authorization: Bearer alice-token' -d '<jsonrpc-initialize>' http://" + addr + "/")

	errCh := make(chan error, 1)
	go func() {
		if sErr := httpSrv.ListenAndServe(); sErr != nil && !errors.Is(sErr, http.ErrServerClosed) {
			errCh <- sErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case sErr := <-errCh:
		return sErr
	}
}

// verify is the TokenVerifier wired into auth.RequireBearerToken. It
// must return auth.ErrInvalidToken (or an error that unwraps to it)
// when the bearer is unknown so the SDK produces the correct 401.
// Tenant lands on TokenInfo.Extra where Layer 2 can pick it up.
func verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	p, ok := validTokens[token]
	if !ok {
		return nil, fmt.Errorf("unknown bearer: %w", auth.ErrInvalidToken)
	}
	// The SDK requires TokenInfo.Expiration so session-lifetime handling
	// has a deadline to work with. A real verifier reads this from the
	// JWT exp claim; we hardcode one hour for this demo.
	return &auth.TokenInfo{
		UserID:     p.UserID,
		Expiration: time.Now().Add(time.Hour),
		Extra:      map[string]any{"tenant": p.Tenant, "token": token},
	}, nil
}

// tokenInfoToMetadata bridges the SDK auth layer to the upstream gRPC
// server by reading the SDK-stashed TokenInfo off ctx and writing the
// fields the server's PrincipalInterceptor already expects.
func tokenInfoToMetadata() protomcp.Middleware {
	return func(next protomcp.Handler) protomcp.Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
			info := auth.TokenInfoFromContext(ctx)
			if info == nil {
				return nil, fmt.Errorf("no TokenInfo on ctx (is auth.RequireBearerToken wired up?)")
			}
			g.Metadata.Set("x-user-id", info.UserID)
			if t, ok := info.Extra["tenant"].(string); ok {
				g.Metadata.Set("x-tenant", t)
			}
			// Optional: forward the raw bearer to let the upstream gRPC
			// server do its own validation.
			//
			// SECURITY: this is a confused-deputy primitive. The token
			// carries whatever audience and scope the issuer stamped on
			// it — if the upstream server does NOT validate the `aud`
			// claim against itself (and enforce `scope`), any audience
			// with a valid token effectively gets whatever that server
			// exposes. Only forward the raw token when the upstream is
			// in the same trust boundary AND audience-validates. For
			// cross-trust deployments, use token exchange (RFC 8693)
			// to mint a new audience-bound token, or forward only the
			// attested claims (user id, scopes) over a trusted channel
			// (mTLS or signed headers) and let the upstream trust them
			// because of the transport, not the bearer.
			if t, ok := info.Extra["token"].(string); ok {
				g.Metadata.Set("x-token", t)
			}
			return next(ctx, req, g)
		}
	}
}

func startAuthGRPC(ctx context.Context) (authv1.ProfileClient, func(), error) {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(authserver.PrincipalInterceptor()))
	authv1.RegisterProfileServer(grpcSrv, authserver.New())
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		grpcSrv.Stop()
		_ = lis.Close()
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	cleanup := func() {
		_ = conn.Close()
		grpcSrv.GracefulStop()
	}
	return authv1.NewProfileClient(conn), cleanup, nil
}
