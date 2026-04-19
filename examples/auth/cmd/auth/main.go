// Command auth runs the protomcp auth example as a standalone HTTP
// server. It demonstrates the two-layer pattern:
//
//   - Layer 1: a stdlib http.Handler middleware validates a bearer
//     token against a hardcoded map and stashes the principal on ctx.
//   - Layer 2: a protomcp.Middleware reads the principal from ctx and
//     writes x-user-id / x-tenant into the outgoing gRPC metadata.
//
// The upstream gRPC server's own PrincipalInterceptor reads those
// metadata keys back out — zero MCP-awareness on the gRPC side.
//
// Try it:
//
//	go run ./examples/auth/cmd/auth &
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type principal struct{ UserID, Tenant string }

type principalKey struct{}

// validTokens is the hardcoded bearer-token -> principal table this
// demo uses in lieu of a real identity provider.
var validTokens = map[string]principal{
	"Bearer alice-token": {UserID: "alice", Tenant: "acme"},
	"Bearer bob-token":   {UserID: "bob", Tenant: "globex"},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server (loopback by default; set 0.0.0.0:PORT for all interfaces)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr)
	stop()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	grpcClient, shutdownGRPC, err := startAuthGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// Layer 2: protomcp.Middleware propagates the ctx principal as
	// outgoing gRPC metadata so the server's interceptor can read it.
	srv := protomcp.New("auth-mcp", "0.1.0",
		protomcp.WithMiddleware(principalToMetadata()),
	)
	authv1.RegisterProfileMCPTools(srv, grpcClient)

	// Layer 1: stdlib http.Handler middleware validates the bearer
	// token and stashes the principal on the request context.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           httpAuth(srv),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("auth-mcp listening on %s\n", addr)
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

func httpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := validTokens[r.Header.Get("Authorization")]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func principalToMetadata() protomcp.Middleware {
	return func(next protomcp.Handler) protomcp.Handler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
			p, ok := ctx.Value(principalKey{}).(principal)
			if !ok {
				return nil, fmt.Errorf("principal missing on ctx")
			}
			g.Metadata.Set("x-user-id", p.UserID)
			g.Metadata.Set("x-tenant", p.Tenant)
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
