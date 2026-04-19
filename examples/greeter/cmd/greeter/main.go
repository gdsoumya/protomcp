// Command greeter runs the protomcp greeter example as a standalone
// HTTP server. The Greeter gRPC service runs in-process on a loopback
// listener; protomcp exposes its annotated RPCs as MCP tools over the
// streamable-HTTP transport.
//
// Usage:
//
//	go run ./examples/greeter/cmd/greeter              # listens on :8080
//	go run ./examples/greeter/cmd/greeter -addr :9000  # listens on :9000
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

	greeterserver "github.com/gdsoumya/protomcp/examples/greeter/server"
	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

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
		log.Fatalf("greeter: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	grpcClient, shutdownGRPC, err := startGreeterGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	srv := protomcp.New("greeter-mcp", "0.1.0")
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("greeter-mcp listening on %s (tools: Greeter_SayHello, Greeter_StreamGreetings)\n", addr)

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

// startGreeterGRPC boots the Greeter gRPC service on a random loopback
// port and returns a connected client plus a cleanup func.
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
