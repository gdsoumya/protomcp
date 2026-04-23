// Command tasks runs the protomcp tasks example as a standalone HTTP
// server. The Tasks gRPC service runs in-process on a loopback listener;
// protomcp exposes its annotated RPCs as MCP tools, resources, and
// prompts over streamable-HTTP. The resources/list surface demonstrates
// cursor-based pagination via protomcp.OffsetPagination.
//
// Usage:
//
//	go run ./examples/tasks/cmd/tasks              # listens on 127.0.0.1:8080
//	go run ./examples/tasks/cmd/tasks -addr :9000  # listens on :9000
//	go run ./examples/tasks/cmd/tasks -page-size 5 # 5 resources per MCP list page
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

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for the MCP server")
	pageSize := flag.Int("page-size", 3, "resources/list page size for OffsetPagination")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr, *pageSize)
	stop()
	if err != nil {
		log.Fatalf("tasks: %v", err)
	}
}

func run(ctx context.Context, addr string, pageSize int) error {
	grpcClient, shutdownGRPC, err := startTasksGRPC(ctx)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// OffsetPagination maps the opaque MCP cursor onto the
	// Tasks.ListAllResources RPC's limit/offset fields (see
	// proto/examples/tasks/v1/tasks.proto). Clients call resources/list
	// without a cursor for page 1, then forward NextCursor from each
	// response until it comes back empty. A single ListAllResources RPC
	// handles both tasks and tags, MCP's resources/list is one flat
	// cursor-paginated stream, so the cumulative view happens gRPC-side.
	listMW, listProc := protomcp.OffsetPagination("limit", "offset", pageSize)

	srv := protomcp.New("tasks-mcp", "0.1.0",
		protomcp.WithResourceListMiddleware(listMW),
		protomcp.WithResourceListResultProcessor(listProc),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	tasksv1.RegisterTasksMCPPrompts(srv, grpcClient)
	// Start the resource_list_changed watchers. The ctx bounds the
	// watcher goroutines; when the signal-handler cancels ctx the
	// watchers exit cleanly.
	tasksv1.StartTasksMCPResourceListChangedWatchers(ctx, srv, grpcClient)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("tasks-mcp listening on %s\n", addr)
	fmt.Println("  tools:     Tasks_ListTasks (read_only), Tasks_GetTask (read_only),")
	fmt.Println("             Tasks_CreateTask, Tasks_UpdateTask (idempotent),")
	fmt.Println("             Tasks_DeleteTask (idempotent, destructive)")
	fmt.Println("  templates: tasks://{id}, tags://{id}")
	fmt.Printf("  list:      Tasks_ListAllResources covers both schemes via {type}://{id}\n")
	fmt.Printf("             (OffsetPagination, page_size=%d)\n", pageSize)
	fmt.Println("  prompts:   tasks_review")
	fmt.Println("  subscribe: user-wired, see examples/subscriptions")
	fmt.Println("  watchers:  Tasks_WatchResourceChanges (resource_list_changed)")
	fmt.Println("            , fires notifications/resources/list_changed per CRUD edit")

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

func startTasksGRPC(ctx context.Context) (tasksv1.TasksClient, func(), error) {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	impl := tasksserver.New()
	// Seed a handful of tags so the multi-type resource_list demo has
	// something to enumerate on a cold start.
	for _, name := range []string{"urgent", "blocked", "needs-review"} {
		impl.SeedTag(name)
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
