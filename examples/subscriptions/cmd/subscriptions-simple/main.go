// Command subscriptions-simple runs a tasks-mcp server using the
// minimal subscription pattern documented by the MCP Go SDK's own
// conformance server: supply no-op SubscribeHandler and
// UnsubscribeHandler, then call srv.SDK().ResourceUpdated(...) from
// the write path whenever a resource changes. The SDK tracks which
// session subscribed to which URI internally and fans each
// ResourceUpdated call out to only those sessions.
//
// Compare this to cmd/subscriptions, which adds a Hub + Manager on
// top. That indirection is only needed when change events come from
// outside the process (pub/sub, CDC feed, upstream gRPC stream, PG
// LISTEN, webhook); for push-from-the-write-path the pattern below
// is the one to copy.
//
// Usage:
//
//	go run ./examples/subscriptions/cmd/subscriptions-simple            # listens on 127.0.0.1:8080
//	go run ./examples/subscriptions/cmd/subscriptions-simple -addr :9000
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr)
	stop()
	if err != nil {
		log.Fatalf("subscriptions-simple: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	// 1. Start the Tasks gRPC service. Its OnChange hook fires on every
	//    CRUD mutation and becomes our push point below.
	tSrv := tasksserver.New()
	grpcClient, shutdownGRPC, err := startTasksGRPC(ctx, tSrv)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// 2. Build the MCP server. No-op Subscribe / Unsubscribe handlers
	//    are what the MCP Go SDK's own conformance server uses: the
	//    SDK tracks per-session subscriptions internally, the handlers
	//    only need to gate or veto the subscribe (return an error to
	//    reject). Returning nil means "allow".
	srv := protomcp.New("tasks-subscriptions-simple-mcp", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   func(context.Context, *mcp.SubscribeRequest) error { return nil },
			UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	// 3. Push path: on every mutation, tell the SDK the matching URI
	//    was updated. The SDK fans out to whichever sessions subscribed
	//    to that URI; non-subscribed sessions and non-matching URIs are
	//    ignored for free. If no client is subscribed the call is a
	//    cheap no-op.
	tSrv.OnChange = func(id string) {
		uri := "tasks://" + id
		if nErr := srv.SDK().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); nErr != nil {
			log.Printf("ResourceUpdated %s: %v", uri, nErr)
		}
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("tasks-subscriptions-simple-mcp listening on %s\n", addr)
	fmt.Println("  resources: tasks://{id} (read + list + push-on-mutation subscribe)")
	fmt.Println("  tools:     Tasks_ListTasks, Tasks_GetTask, Tasks_CreateTask,")
	fmt.Println("             Tasks_UpdateTask, Tasks_DeleteTask")

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

func startTasksGRPC(ctx context.Context, impl tasksv1.TasksServer) (tasksv1.TasksClient, func(), error) {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, impl)
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
	return tasksv1.NewTasksClient(conn), cleanup, nil
}
