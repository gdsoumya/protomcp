// Command subscriptions runs a tasks-mcp server that demonstrates
// user-wired resource subscriptions on top of the generator-exposed
// tasks://{id} resource. Every CRUD write on the underlying gRPC
// Tasks service publishes to an in-process Hub; MCP clients subscribing
// to tasks://{id} receive resources/updated notifications via a user-
// owned SubscribeHandler that protomcp never sees the details of.
//
// Usage:
//
//	go run ./examples/subscriptions/cmd/subscriptions            # listens on 127.0.0.1:8080
//	go run ./examples/subscriptions/cmd/subscriptions -addr :9000
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

	"github.com/gdsoumya/protomcp/examples/subscriptions"
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
		log.Fatalf("subscriptions: %v", err)
	}
}

func run(ctx context.Context, addr string) error {
	// In-process hub, stands in for a backend notification source
	// (pub/sub, CDC feed, bus consumer, DB triggers). Everything else
	// connects via Publish/Subscribe on Hub.
	hub := subscriptions.NewHub()

	// Tasks gRPC service on a loopback listener. OnChange is the write
	// path's hook for publishing per-entity topics into the Hub.
	tSrv := tasksserver.New()
	tSrv.OnChange = func(id string) {
		hub.Publish("tasks://" + id)
	}
	grpcClient, shutdownGRPC, err := startTasksGRPC(ctx, tSrv)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// The Manager is constructed before *protomcp.Server, so the
	// notifier closes over srv and is only invoked from goroutines
	// spawned inside Subscribe, which cannot run before the server is
	// listening.
	var srv *protomcp.Server
	notifier := subscriptions.Notifier(func(ctx context.Context, p *mcp.ResourceUpdatedNotificationParams) error {
		return srv.SDK().ResourceUpdated(ctx, p)
	})

	mgr, err := subscriptions.NewManager(hub, "tasks://{id}", notifier)
	if err != nil {
		return fmt.Errorf("new subscription manager: %w", err)
	}
	srv = protomcp.New("tasks-subscriptions-mcp", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   mgr.Subscribe,
			UnsubscribeHandler: mgr.Unsubscribe,
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("tasks-subscriptions-mcp listening on %s\n", addr)
	fmt.Println("  resources: tasks://{id} (read + list + user-wired subscribe)")
	fmt.Println("  tools:     Tasks_ListTasks, Tasks_GetTask, Tasks_CreateTask,")
	fmt.Println("             Tasks_UpdateTask, Tasks_DeleteTask")
	fmt.Println("  subscribe: powered by Hub.Publish via Tasks.OnChange, see manager.go")

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
