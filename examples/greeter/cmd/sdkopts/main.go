// Command sdkopts runs the protomcp greeter example with both
// WithSDKOptions and WithHTTPOptions enabled:
//
//   - SDK Logger writes server-side activity to stderr via slog.
//   - SDK Instructions populates the server-info "instructions" block.
//   - HTTP transport is flipped to JSONResponse mode, so responses come
//     back as application/json rather than text/event-stream.
//
// Exercise it:
//
//	curl -sv -H 'Content-Type: application/json' \
//	     -H 'Accept: application/json, text/event-stream' \
//	     -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
//	          "params":{"protocolVersion":"2025-06-18","capabilities":{},
//	                    "clientInfo":{"name":"c","version":"0"}}}' \
//	     http://localhost:8080/
//
// Content-Type on the response will be application/json (not
// text/event-stream) because JSONResponse is on.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	greeterserver "github.com/gdsoumya/protomcp/examples/greeter/server"
	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server (loopback by default; set 0.0.0.0:PORT for all interfaces)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr)
	stop()
	if err != nil {
		log.Fatalf("sdkopts: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	grpcClient, shutdownGRPC, err := startGreeterGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv := protomcp.New("greeter-sdkopts", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			Logger:       logger,
			Instructions: "Demo server: call Greeter_SayHello to see JSON-response mode in action.",
		}),
		protomcp.WithHTTPOptions(&mcp.StreamableHTTPOptions{
			JSONResponse: true,
		}),
	)
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("greeter-sdkopts listening on %s (JSONResponse=true, SDK Logger=stderr)\n", addr)
	fmt.Println("initialize request shape: POST /  Content-Type: application/json  Accept: application/json, text/event-stream")
	fmt.Println("response shape:           Content-Type: application/json (not text/event-stream)")

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

func startGreeterGRPC(ctx context.Context) (greeterv1.GreeterClient, func(), error) {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	grpcSrv := grpc.NewServer()
	greeterv1.RegisterGreeterServer(grpcSrv, greeterserver.New())
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
	return greeterv1.NewGreeterClient(conn), cleanup, nil
}
