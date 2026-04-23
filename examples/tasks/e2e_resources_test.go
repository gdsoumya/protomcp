// Package tasks_test, MCP resource-surface coverage. These tests are
// separate from e2e_test.go so the tool CRUD assertions stay
// standalone; they share the gRPC bring-up helpers via the package-
// local startGRPC / connect funcs.
package tasks_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

// startGRPCWithServer boots the Tasks gRPC service and returns both
// the client and the underlying tasksserver.Server so tests can seed
// state through helpers like SeedTag that aren't exposed on the gRPC
// surface. Like startGRPC in e2e_test.go but hands the impl back.
func startGRPCWithServer(t *testing.T) (tasksv1.TasksClient, *tasksserver.Server) {
	t.Helper()
	impl := tasksserver.New()
	grpcClient := clientFromServer(t, impl)
	return grpcClient, impl
}

// clientFromServer wires a gRPC client to an existing tasksserver.Server
// running on a random loopback port. Test bodies that need to inspect
// the server AND call through gRPC use this to get both handles in one
// Cleanup scope.
func clientFromServer(t *testing.T, impl *tasksserver.Server) tasksv1.TasksClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, impl)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() { grpcSrv.Stop(); _ = lis.Close() })
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return tasksv1.NewTasksClient(conn)
}

// newResourceServer brings up a full-surface server: tools + resources.
// Returns a connected client session and the backing gRPC Tasks server
// so tests can trigger changes (to fire subscribe notifications)
// without going through the MCP path.
func newResourceServer(ctx context.Context, t *testing.T, opts ...protomcp.ServerOption) (*mcp.ClientSession, tasksv1.TasksClient) {
	t.Helper()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0", opts...)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	cs := connect(ctx, t, srv)
	return cs, grpcClient
}

// TestResourceList_MultiTypeWithOffsetPagination demonstrates the
// single-annotation `resources/list` pattern: one gRPC RPC
// (ListAllResources) returns both tasks and tags; the URI template
// `{type}://{id}` expands the scheme per item. Seeds 3 tasks + 2 tags,
// paginates at size 2 → expects 3 pages (2, 2, 1) with mixed schemes,
// and asserts every returned URI matches one of the two schemes.
func TestResourceList_MultiTypeWithOffsetPagination(t *testing.T) {
	ctx := context.Background()
	grpcClient, tImpl := startGRPCWithServer(t)

	// Seed directly on the server so tags are available without needing a
	// CreateTag gRPC surface (the demo intentionally only exposes GetTag
	// as a resource template, tags are static labels).
	tImpl.SeedTag("urgent")
	tImpl.SeedTag("blocked")
	for i := range 3 {
		_, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
			Task: &tasksv1.Task{Title: fmt.Sprintf("task-%d", i)},
		})
		if err != nil {
			t.Fatalf("seed create: %v", err)
		}
	}

	mw, proc := protomcp.OffsetPagination("limit", "offset", 2)
	srv := protomcp.New("tasks", "0.1.0",
		protomcp.WithResourceListMiddleware(mw),
		protomcp.WithResourceListResultProcessor(proc),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	cs := connect(ctx, t, srv)

	// 5 total items, 2 per page → pages of (2, 2, 1).
	var all []*mcp.Resource
	var cursor string
	for page := 1; ; page++ {
		var params *mcp.ListResourcesParams
		if cursor != "" {
			params = &mcp.ListResourcesParams{Cursor: cursor}
		}
		res, err := cs.ListResources(ctx, params)
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		all = append(all, res.Resources...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
		if page > 10 {
			t.Fatalf("pagination did not terminate after 10 pages")
		}
	}
	if got, want := len(all), 5; got != want {
		t.Errorf("total resources = %d, want %d", got, want)
	}

	// Each URI starts with tasks:// or tags://; each Name is non-empty
	// (rendered from {{name}}, which maps to task.title / tag.name
	// server-side).
	seenSchemes := map[string]int{}
	for _, r := range all {
		switch {
		case strings.HasPrefix(r.URI, "tasks://"):
			seenSchemes["tasks"]++
		case strings.HasPrefix(r.URI, "tags://"):
			seenSchemes["tags"]++
		default:
			t.Errorf("resource URI %q has neither tasks:// nor tags:// scheme", r.URI)
		}
		if r.Name == "" {
			t.Errorf("resource %q has empty Name, Mustache render failed", r.URI)
		}
	}
	if seenSchemes["tasks"] != 3 {
		t.Errorf("tasks scheme count = %d, want 3", seenSchemes["tasks"])
	}
	if seenSchemes["tags"] != 2 {
		t.Errorf("tags scheme count = %d, want 2", seenSchemes["tags"])
	}
}

// TestResourceRead_TagTemplate, the tags://{id} resource template
// resolves via GetTag. Verifies that a second resource_template
// annotation on the same service is reachable alongside the first.
func TestResourceRead_TagTemplate(t *testing.T) {
	ctx := context.Background()
	_, tImpl := startGRPCWithServer(t)
	seeded := tImpl.SeedTag("urgent")

	grpcClient := clientFromServer(t, tImpl)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	cs := connect(ctx, t, srv)

	uri := "tags://" + seeded.GetId()
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read %q: %v", uri, err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(res.Contents))
	}
	if res.Contents[0].URI != uri {
		t.Errorf("content URI = %q, want %q", res.Contents[0].URI, uri)
	}
	var got tasksv1.Tag
	if err := protojson.Unmarshal([]byte(res.Contents[0].Text), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.GetName() != "urgent" {
		t.Errorf("tag name = %q, want urgent", got.GetName())
	}
}

// TestResourceRead_HappyPath, create a task, pick its URI from the
// list, read it back, assert fields round-trip via protojson.
func TestResourceRead_HappyPath(t *testing.T) {
	ctx := context.Background()
	cs, grpcClient := newResourceServer(ctx, t)

	created, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "read-me", Description: "for the read test"},
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	uri := "tasks://" + created.GetId()
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != uri {
		t.Errorf("content URI = %q, want %q", c.URI, uri)
	}
	if c.MIMEType != "application/json" {
		t.Errorf("content MIMEType = %q, want application/json", c.MIMEType)
	}
	if c.Text == "" {
		t.Fatalf("content Text is empty")
	}
	var got tasksv1.Task
	if err := protojson.Unmarshal([]byte(c.Text), &got); err != nil {
		t.Fatalf("protojson.Unmarshal %q: %v", c.Text, err)
	}
	if got.GetId() != created.GetId() {
		t.Errorf("task id mismatch: got %q want %q", got.GetId(), created.GetId())
	}
	if got.GetTitle() != "read-me" || got.GetDescription() != "for the read test" {
		t.Errorf("task payload mismatch: %+v", &got)
	}
}

// TestResourceListChanged_FiresOnMutation, the generated
// StartTasksMCPResourceListChangedWatchers spawns a goroutine that
// opens WatchResourceChanges; every CRUD mutation fires a stream
// event which the watcher forwards to Server.NotifyResourceListChanged,
// which the SDK debounces and broadcasts as
// notifications/resources/list_changed. A client that installs
// ResourceListChangedHandler observes the notification arriving.
func TestResourceListChanged_FiresOnMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grpcClient, _ := startGRPCWithServer(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	tasksv1.StartTasksMCPResourceListChangedWatchers(ctx, srv, grpcClient)

	// Client with a ResourceListChangedHandler installed so we can
	// observe the notification.
	changed := make(chan struct{}, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ClientOptions{
		ResourceListChangedHandler: func(_ context.Context, _ *mcp.ResourceListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
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

	// Give the watcher goroutine a moment to open the stream before
	// mutating (otherwise the mutation fires before there's anyone
	// subscribed to the change feed, and the tick is dropped).
	time.Sleep(100 * time.Millisecond)

	// Trigger a mutation.
	if _, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
		Task: &tasksv1.Task{Title: "tickles-list-changed"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no notifications/resources/list_changed within 1s of CreateTask")
	}
}

// TestResourceListChanged_Debounces, multiple rapid mutations
// coalesce to at most one notification per SDK debounce window
// (~10ms). Fire 10 creates in a tight loop, assert we see roughly
// one notification (≥1 and ≤ a small cap), not 10.
func TestResourceListChanged_Debounces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grpcClient, _ := startGRPCWithServer(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)
	tasksv1.StartTasksMCPResourceListChangedWatchers(ctx, srv, grpcClient)

	var count atomic.Int32
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ClientOptions{
		ResourceListChangedHandler: func(_ context.Context, _ *mcp.ResourceListChangedRequest) {
			count.Add(1)
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

	time.Sleep(100 * time.Millisecond) // watcher open

	for i := range 10 {
		if _, err := grpcClient.CreateTask(ctx, &tasksv1.CreateTaskRequest{
			Task: &tasksv1.Task{Title: fmt.Sprintf("burst-%d", i)},
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Wait for the debounce window to elapse plus a margin.
	time.Sleep(200 * time.Millisecond)

	got := count.Load()
	if got < 1 {
		t.Errorf("got %d notifications, want ≥1", got)
	}
	if got >= 10 {
		t.Errorf("got %d notifications for 10 mutations; SDK debounce should collapse them", got)
	}
}
