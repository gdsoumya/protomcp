// Command errorhandler runs the protomcp greeter example with a
// custom ErrorHandler. NotFound gRPC errors get a friendlier
// IsError text; all other codes delegate to DefaultErrorHandler so the
// standard mapping still applies (JSON-RPC error for
// Unauthenticated/PermissionDenied/Canceled/DeadlineExceeded; IsError
// CallToolResult otherwise).
//
// Drive it by calling Greeter_FailWith with {"code":5,"message":"..."}
// to see the custom NotFound message, and any other code to see the
// default behavior.
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// friendlyNotFound wraps the default handler: gRPC NotFound errors are
// rewritten as a friendly tool-result; everything else falls through.
func friendlyNotFound(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{
				Text: "resource not found; try a different id",
			}},
		}, nil
	}
	return protomcp.DefaultToolErrorHandler(ctx, req, err)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server (loopback by default; set 0.0.0.0:PORT for all interfaces)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr)
	stop()
	if err != nil {
		log.Fatalf("errorhandler: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	grpcClient, shutdownGRPC, err := startGreeterGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	srv := protomcp.New("greeter-errorhandler", "0.1.0",
		protomcp.WithToolErrorHandler(friendlyNotFound),
	)
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("greeter-errorhandler listening on %s (call Greeter_FailWith with code=5 for the custom message)\n", addr)

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
