package subscriptions_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/gdsoumya/protomcp/examples/subscriptions"
	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

// setup brings up a full MCP server wired through the subscriptions
// example: tasks gRPC + Hub + Manager + SubscribeHandlers. Returns a
// connected MCP client session, the gRPC Tasks client, and a channel
// the test can drain for resources/updated notifications.
func setup(ctx context.Context, t *testing.T) (*mcp.ClientSession, tasksv1.TasksClient, <-chan string) {
	t.Helper()

	// In-process gRPC Tasks service with OnChange publishing to the Hub.
	tImpl := tasksserver.New()
	hub := subscriptions.NewHub()
	tImpl.OnChange = func(id string) { hub.Publish("tasks://" + id) }

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, tImpl)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() { grpcSrv.GracefulStop(); _ = lis.Close() })

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	grpcClient := tasksv1.NewTasksClient(conn)

	// Manager + server. Two-phase notifier wiring: srv doesn't exist
	// yet when we construct the Manager, so the notifier closes over
	// a pointer we set immediately after New().
	var srv *protomcp.Server
	mgr, err := subscriptions.NewManager(hub, "tasks://{id}",
		func(ctx context.Context, p *mcp.ResourceUpdatedNotificationParams) error {
			return srv.SDK().ResourceUpdated(ctx, p)
		})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	srv = protomcp.New("tasks-subs-test", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   mgr.Subscribe,
			UnsubscribeHandler: mgr.Unsubscribe,
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	// In-memory transport pair, easier to reason about than streamable-HTTP
	// for this test.
	updates := make(chan string, 16)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"},
		&mcp.ClientOptions{
			ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
				updates <- req.Params.URI
			},
		})

	cT, sT := mcp.NewInMemoryTransports()
	ss, err := srv.SDK().Connect(ctx, sT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs, grpcClient, updates
}

// TestSubscribe_UpdateFires, subscribe to a task URI, mutate the task,
// assert a resources/updated arrives. This is the golden-path check.
func TestSubscribe_UpdateFires(t *testing.T) {
	ctx := context.Background()
	cs, grpcClient, updates := setup(ctx, t)

	created, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "watch-me"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uri := "tasks://" + created.GetId()

	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Give the subscribe goroutine a moment to hook into the Hub.
	time.Sleep(50 * time.Millisecond)

	if _, err := grpcClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id: created.GetId(), Title: "watch-me", Done: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	select {
	case got := <-updates:
		if got != uri {
			t.Errorf("updated URI = %q, want %q", got, uri)
		}
	case <-time.After(time.Second):
		t.Fatal("no resources/updated notification within 1s")
	}
}

// TestUnsubscribe_StopsNotifications, subscribe, trigger one update to
// prove the path works, unsubscribe, trigger another update, assert
// nothing more arrives.
func TestUnsubscribe_StopsNotifications(t *testing.T) {
	ctx := context.Background()
	cs, grpcClient, updates := setup(ctx, t)

	created, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "unsub-me"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uri := "tasks://" + created.GetId()

	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Baseline: one update should arrive.
	if _, err := grpcClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id: created.GetId(), Title: "unsub-me", Done: true,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("baseline update did not deliver")
	}

	// Unsubscribe and confirm no further updates deliver.
	if err := cs.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	// Give the unsubscribe a beat to tear down the Hub subscription.
	time.Sleep(50 * time.Millisecond)

	if _, err := grpcClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id: created.GetId(), Title: "unsub-me", Done: false,
	}); err != nil {
		t.Fatalf("second update: %v", err)
	}

	select {
	case got := <-updates:
		t.Errorf("received unexpected update %q after unsubscribe", got)
	case <-time.After(200 * time.Millisecond):
		// Good: nothing delivered.
	}
}

// TestSessionClose_Cleanup, subscribe, close the client session
// WITHOUT sending Unsubscribe, assert the Hub subscription is torn down
// by the session watchdog. We observe this indirectly: after session
// close, publishing to the topic should not panic on a dead channel and
// the Hub should report zero subscribers for the topic.
func TestSessionClose_Cleanup(t *testing.T) {
	ctx := context.Background()

	// Stand up server + hub manually so the test can inspect Hub state.
	tImpl := tasksserver.New()
	hub := subscriptions.NewHub()
	var notifyCalls atomic.Int32
	tImpl.OnChange = func(id string) { hub.Publish("tasks://" + id) }

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, tImpl)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() { grpcSrv.GracefulStop(); _ = lis.Close() })

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	grpcClient := tasksv1.NewTasksClient(conn)

	var srv *protomcp.Server
	mgr, err := subscriptions.NewManager(hub, "tasks://{id}",
		func(ctx context.Context, p *mcp.ResourceUpdatedNotificationParams) error {
			notifyCalls.Add(1)
			return srv.SDK().ResourceUpdated(ctx, p)
		})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	srv = protomcp.New("tasks-subs-test", "0.1.0",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   mgr.Subscribe,
			UnsubscribeHandler: mgr.Unsubscribe,
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cT, sT := mcp.NewInMemoryTransports()
	ss, err := srv.SDK().Connect(ctx, sT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := client.Connect(ctx, cT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	created, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "cleanup-me"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uri := "tasks://" + created.GetId()

	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Close the session WITHOUT Unsubscribe. The watchdog must clean up.
	_ = cs.Close()
	_ = ss.Close()

	// Give the watchdog a moment to fire.
	time.Sleep(100 * time.Millisecond)

	// A Publish for the old URI should no longer reach the Manager ,
	// the Hub has no subscribers. We can't probe Hub internals directly,
	// but we can observe that no further notifier calls happen after a
	// Publish. Baseline the counter first, then Publish, then wait.
	before := notifyCalls.Load()
	hub.Publish(uri)
	time.Sleep(100 * time.Millisecond)
	if after := notifyCalls.Load(); after != before {
		t.Errorf("notifier fired %d times after session close; want %d", after, before)
	}
}

// TestSubscribe_DuplicateDoesNotLeak, a second Subscribe for the same
// (session, URI) must cancel the prior Hub subscription rather than
// leak it. Observable effect: after the second subscribe, publishing
// once produces exactly one notification (not two).
func TestSubscribe_DuplicateDoesNotLeak(t *testing.T) {
	ctx := context.Background()
	cs, grpcClient, updates := setup(ctx, t)

	created, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "dup-me"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	uri := "tasks://" + created.GetId()

	// Subscribe twice in a row.
	for i := range 2 {
		if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
			t.Fatalf("subscribe #%d: %v", i, err)
		}
	}
	time.Sleep(50 * time.Millisecond)

	// Trigger one backend mutation; count notifications the client sees.
	if _, err := grpcClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id: created.GetId(), Title: "dup-me", Done: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Drain a short window and assert exactly one notification.
	count := 0
	deadline := time.After(300 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-updates:
			count++
		case <-deadline:
			break drainLoop
		}
	}
	if count != 1 {
		t.Errorf("duplicate subscribe delivered %d notifications; want 1 (prior watcher leaked)", count)
	}
}
