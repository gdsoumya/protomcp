// Command subscriptions runs a tasks-mcp server that demonstrates
// user-wired resource subscriptions backed by a long-lived watch
// stream plus per-principal subscribe ACLs.
//
// Shape:
//
//   - The Tasks gRPC service exposes an OnChange hook that simulates
//     what a server-streaming Watch RPC would emit: one task-id event
//     per mutation.
//   - A single watcher goroutine reads those events and calls
//     srv.SDK().ResourceUpdated for each. The MCP Go SDK's internal
//     subscription map handles per-session fan-out.
//   - The watcher is started lazily, exactly once, on the first
//     authorized subscribe (sync.Once). It runs inside
//     protomcp.RetryLoop so a transient stream failure backs off and
//     reconnects.
//   - Subscribe goes through authorize() (see authz.go), which reads
//     the *principal stashed on ctx by authMiddleware and rejects
//     callers whose ACL does not cover the requested URI.
//   - Unsubscribe is a no-op; the SDK drops the session from its
//     subscriptions map automatically.
//
// Two demo tokens:
//
//	alice-token -> may subscribe to any tasks://*
//	bob-token   -> may subscribe only to tasks://1
//
// Usage:
//
//	go run ./examples/subscriptions/cmd/subscriptions            # listens on 127.0.0.1:8080
//	go run ./examples/subscriptions/cmd/subscriptions -addr :9000
//
// All requests must carry an Authorization: Bearer <token> header.
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
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

// eventBuffer caps how many un-drained events the OnChange hook will
// enqueue before it starts dropping. With no subscriptions active the
// watcher isn't running and drops are expected; once subscribed, the
// watcher drains faster than most backends produce.
const eventBuffer = 16

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
	// Tasks gRPC service on a loopback listener. OnChange is the
	// event source; in a real deployment this is whatever feeds your
	// Watch stream (pub/sub, CDC, polling, a grpc ServerStream).
	tSrv := tasksserver.New()
	events := make(chan string, eventBuffer)
	tSrv.OnChange = func(id string) {
		select {
		case events <- id:
		default:
			// Drop when nobody is reading: before the first subscribe
			// lands there is no watcher. The SDK's subscription map is
			// empty anyway so a dropped event would have fanned out to
			// zero sessions.
		}
	}
	grpcClient, shutdownGRPC, err := startTasksGRPC(ctx, tSrv)
	if err != nil {
		return fmt.Errorf("start grpc: %w", err)
	}
	defer shutdownGRPC()

	// Two-phase notifier: srv is built below, but the watcher closure
	// needs to call srv.SDK().ResourceUpdated. The closure captures
	// &srv (a pointer to a variable) and the goroutine is only spawned
	// after srv is assigned, by sync.Once on the first authorized
	// subscribe.
	var srv *protomcp.Server
	var startWatcher sync.Once
	watch := func() {
		go protomcp.RetryLoop(ctx, func(ctx context.Context, reset func()) error {
			// Simulated watch-stream open. A real implementation might
			// be:
			//
			//	stream, err := grpcClient.Watch(ctx, &WatchRequest{})
			//	if err != nil { return err }
			//
			// On connect, reset() tells RetryLoop the backoff has
			// succeeded so any later failure starts fresh.
			reset()
			for {
				select {
				case <-ctx.Done():
					return nil
				case id, ok := <-events:
					if !ok {
						return errors.New("event stream closed")
					}
					uri := "tasks://" + id
					if nErr := srv.SDK().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); nErr != nil {
						log.Printf("ResourceUpdated %s: %v", uri, nErr)
					}
				}
			}
		})
	}

	// Subscribe handler: authz gate + lazy watcher start. No
	// per-session bookkeeping here; the SDK records (session, URI)
	// and srv.SDK().ResourceUpdated fans out to matching sessions.
	subscribe := authorize(func(context.Context, *mcp.SubscribeRequest) error {
		startWatcher.Do(watch)
		return nil
	})
	// Unsubscribe: the SDK removes the session from its own map; the
	// watcher keeps running for other subscribers (and costs nothing
	// when the map is empty because ResourceUpdated is a no-op then).
	unsubscribe := func(context.Context, *mcp.UnsubscribeRequest) error { return nil }

	srv = protomcp.New("tasks-subscriptions-mcp", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   subscribe,
			UnsubscribeHandler: unsubscribe,
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	httpSrv := &http.Server{
		Addr: addr,
		// authMiddleware resolves the Authorization bearer into a
		// *principal on the request ctx. SubscribeHandler reads it via
		// principalFromContext inside authorize().
		Handler:           authMiddleware(srv),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("tasks-subscriptions-mcp listening on %s\n", addr)
	fmt.Println("  auth:      Bearer alice-token (all tasks) | bob-token (tasks://1 only)")
	fmt.Println("  resources: tasks://{id} (read + list + ACL-gated subscribe)")
	fmt.Println("  watch:     started once on first subscribe, backed by RetryLoop")

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
