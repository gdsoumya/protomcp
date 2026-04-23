// Command mutator runs the protomcp greeter example with a Middleware
// that rewrites the HelloRequest.Name field before the upstream gRPC
// call. This demonstrates GRPCData.Input, mutations persist because
// Input is the same pointer the generated handler forwards.
//
// Invoke Greeter_SayHello with {"name":"alice"} and the upstream gRPC
// server observes "demo-alice", so the response is
// {"message":"Hello, demo-alice!"}.
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
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server (loopback by default; set 0.0.0.0:PORT for all interfaces)")
	prefix := flag.String("prefix", "demo", "prefix prepended to HelloRequest.Name")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr, *prefix)
	stop()
	if err != nil {
		log.Fatalf("mutator: %v", err)
	}
}

// prefixName is a protomcp.ToolMiddleware that rewrites HelloRequest.Name.
// The type-assertion path is what most code should use; for generic
// handling across many message types, proto reflection works too, e.g.
//
//	m := g.Input.ProtoReflect()
//	fd := m.Descriptor().Fields().ByName("name")
//	m.Set(fd, protoreflect.ValueOfString(prefix+"-"+m.Get(fd).String()))
func prefixName(prefix string) protomcp.ToolMiddleware {
	return func(next protomcp.ToolHandler) protomcp.ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCData) (*mcp.CallToolResult, error) {
			if r, ok := g.Input.(*greeterv1.HelloRequest); ok {
				r.Name = prefix + "-" + r.Name
			}
			return next(ctx, req, g)
		}
	}
}

func run(ctx context.Context, addr, prefix string) error {
	grpcClient, shutdownGRPC, err := startGreeterGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	srv := protomcp.New("greeter-mutator", "0.1.0",
		protomcp.WithToolMiddleware(prefixName(prefix)),
	)
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("greeter-mutator listening on %s (rewrites name -> %q-<name>)\n", addr, prefix)

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
