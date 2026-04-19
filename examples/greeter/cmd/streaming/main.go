// Command streaming is a self-contained demonstration of the
// server-streaming → MCP progress notification flow. It wires up the
// Greeter gRPC server, the protomcp server, and an MCP client in one
// process, invokes the StreamGreetings tool, and prints each progress
// notification as it arrives alongside the final tool result.
//
// Run:
//
//	go run ./examples/greeter/cmd/streaming
//
// Expected output (abridged):
//
//	progress: {"message":"Turn 1: hello, world!"}
//	progress: {"message":"Turn 2: hello, world!"}
//	progress: {"message":"Turn 3: hello, world!"}
//	progress: {"message":"Turn 4: hello, world!"}
//	progress: {"message":"Turn 5: hello, world!"}
//	final result: 5 messages; last: {"message":"Turn 5: hello, world!"}
//
// Each progress line corresponds to one streamed gRPC message. The
// final result carries the summary produced by the generated handler
// at EOF.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
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
	name := flag.String("name", "world", "name to greet")
	turns := flag.Int("turns", 5, "number of streamed greetings")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *name, *turns)
	stop()
	if err != nil {
		log.Fatalf("streaming: %v", err)
	}
}

func run(ctx context.Context, name string, turns int) error {
	grpcClient, shutdown, err := startGreeterGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdown()

	srv := protomcp.New("streaming-demo", "0.1.0")
	greeterv1.RegisterGreeterMCPTools(srv, grpcClient)

	// In-memory client/server transports — no sockets, no ports.
	cT, sT := mcp.NewInMemoryTransports()
	ss, err := srv.SDK().Connect(ctx, sT, nil)
	if err != nil {
		return fmt.Errorf("server connect: %w", err)
	}
	defer func() { _ = ss.Close() }()

	// Progress notifications are pushed asynchronously by the SDK; we
	// collect them under a mutex and flush after the call returns so
	// stdout stays in order.
	var (
		mu       sync.Mutex
		progress []string
	)
	client := mcp.NewClient(&mcp.Implementation{Name: "demo-client", Version: "0.1.0"},
		&mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, p *mcp.ProgressNotificationClientRequest) {
				mu.Lock()
				defer mu.Unlock()
				progress = append(progress, p.Params.Message)
			},
		},
	)

	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		return fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = cs.Close() }()

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "Greeter_StreamGreetings",
		Arguments: map[string]any{
			"name":  name,
			"turns": turns,
		},
		// ProgressToken signals we want progress notifications for
		// this call — the generated server-streaming handler keys
		// NotifyProgress off it.
		Meta: mcp.Meta{"progressToken": "streaming-demo"},
	})
	if err != nil {
		return fmt.Errorf("call StreamGreetings: %w", err)
	}
	if result.IsError {
		return fmt.Errorf("tool returned IsError: %+v", result)
	}

	// Progress notifications are async — wait briefly for the tail.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(progress)
		mu.Unlock()
		if got >= turns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	for _, m := range progress {
		fmt.Println("progress:", m)
	}
	mu.Unlock()

	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(*mcp.TextContent); ok {
			fmt.Println("final result:", tc.Text)
		}
	}
	return nil
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
