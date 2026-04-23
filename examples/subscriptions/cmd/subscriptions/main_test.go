package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

// harness is the test double for main.go's run(): the same Tasks gRPC
// service, the same watcher + sync.Once wiring, the same
// authMiddleware-wrapped MCP server, but served from httptest.Server
// so t.Cleanup can tear it down deterministically.
type harness struct {
	url         string
	tasksClient tasksv1.TasksClient
}

func setupHarness(ctx context.Context, t *testing.T) *harness {
	t.Helper()

	tSrv := tasksserver.New()
	events := make(chan string, 16)
	tSrv.OnChange = func(id string) {
		select {
		case events <- id:
		default:
		}
	}
	grpcClient, shutdownGRPC := startTasksGRPCForTest(ctx, t, tSrv)

	var srv *protomcp.Server
	var once sync.Once
	watch := func() {
		go protomcp.RetryLoop(ctx, func(ctx context.Context, reset func()) error {
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
					_ = srv.SDK().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri})
				}
			}
		})
	}
	subscribe := authorize(func(context.Context, *mcp.SubscribeRequest) error {
		once.Do(watch)
		return nil
	})
	unsubscribe := func(context.Context, *mcp.UnsubscribeRequest) error { return nil }

	srv = protomcp.New("tasks-subs-test", "0.0.1",
		protomcp.WithSDKOptions(&mcp.ServerOptions{
			SubscribeHandler:   subscribe,
			UnsubscribeHandler: unsubscribe,
		}),
	)
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPResources(srv, grpcClient)

	httpSrv := httptest.NewServer(authMiddleware(srv))
	t.Cleanup(func() {
		httpSrv.Close()
		shutdownGRPC()
	})
	return &harness{url: httpSrv.URL, tasksClient: grpcClient}
}

func startTasksGRPCForTest(ctx context.Context, t *testing.T, impl tasksv1.TasksServer) (tasksv1.TasksClient, func()) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, impl)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		grpcSrv.Stop()
		_ = lis.Close()
		t.Fatalf("dial: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		grpcSrv.GracefulStop()
	}
	return tasksv1.NewTasksClient(conn), cleanup
}

// connectMCP dials the harness via the MCP streamable-HTTP transport.
// token, if non-empty, is sent as Authorization: Bearer <token> on
// every request. notif is populated with every ResourceUpdated URI
// the SDK receives.
func connectMCP(ctx context.Context, t *testing.T, url, token string, notif chan<- string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{
			ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
				select {
				case notif <- req.Params.URI:
				default:
				}
			},
		},
	)
	tr := &mcp.StreamableClientTransport{Endpoint: url}
	if token != "" {
		tr.HTTPClient = &http.Client{
			Transport: &headerAdderRT{base: http.DefaultTransport, token: token},
		}
	}
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, nil
}

type headerAdderRT struct {
	base  http.RoundTripper
	token string
}

func (r *headerAdderRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+r.token)
	return r.base.RoundTrip(req)
}

// createTask is a small convenience that mutes the tests' noise.
func createTask(ctx context.Context, t *testing.T, client tasksv1.TasksClient, title string) *tasksv1.Task {
	t.Helper()
	task, err := client.CreateTask(ctx, &tasksv1.CreateTaskRequest{Task: &tasksv1.Task{Title: title}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestSubscribe_AuthorizedAlice_ReceivesUpdate exercises the happy
// path: alice-token's ACL permits tasks://*, so a Subscribe followed
// by an UpdateTask flows a resources/updated notification back to the
// client. Also verifies the watcher actually started (sync.Once
// triggered).
func TestSubscribe_AuthorizedAlice_ReceivesUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := setupHarness(ctx, t)

	notif := make(chan string, 8)
	cs, err := connectMCP(ctx, t, h.url, "alice-token", notif)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	task := createTask(ctx, t, h.tasksClient, "alice-owned")
	drain(notif) // discard the CreateTask pre-subscribe event

	uri := "tasks://" + task.GetId()
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := h.tasksClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id:    task.GetId(),
		Title: "renamed",
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	select {
	case got := <-notif:
		if got != uri {
			t.Errorf("ResourceUpdated URI = %q, want %q", got, uri)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ResourceUpdated notification")
	}
}

// TestSubscribe_BobOutOfScope_Rejected verifies the authz wrapper
// refuses a subscription request whose URI falls outside the
// principal's AllowedURIPrefixes. bob-token is scoped to tasks://1
// only; this test uses a URI that is guaranteed not to match that
// prefix (deterministic, so no flakes against a random task id that
// happens to start with "1").
func TestSubscribe_BobOutOfScope_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := setupHarness(ctx, t)

	notif := make(chan string, 4)
	cs, err := connectMCP(ctx, t, h.url, "bob-token", notif)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// "tasks://out-of-bobs-scope" has no prefix overlap with
	// "tasks://1", so authz must reject.
	err = cs.Subscribe(ctx, &mcp.SubscribeParams{URI: "tasks://out-of-bobs-scope"})
	if err == nil {
		t.Fatal("Subscribe succeeded for out-of-scope URI; want permission error")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("Subscribe error = %q; want message naming the denial", err.Error())
	}
}

// TestConnect_NoBearer_Rejected verifies the HTTP-layer bearer gate:
// a request with no Authorization header never reaches the MCP server
// and the client Connect call fails at initialize time.
func TestConnect_NoBearer_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := setupHarness(ctx, t)

	notif := make(chan string, 4)
	_, err := connectMCP(ctx, t, h.url, "" /* no token */, notif)
	if err == nil {
		t.Fatal("Connect succeeded without a bearer token; want 401")
	}
}

// TestSubscribe_MultiSession_FanOut verifies the watcher that was
// started once (via sync.Once on the first Subscribe) serves updates
// to every subscribed session. Both clients subscribe to the same
// URI; a single UpdateTask must notify both.
func TestSubscribe_MultiSession_FanOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := setupHarness(ctx, t)

	task := createTask(ctx, t, h.tasksClient, "shared")
	uri := "tasks://" + task.GetId()

	notif1 := make(chan string, 4)
	cs1, cErr := connectMCP(ctx, t, h.url, "alice-token", notif1)
	if cErr != nil {
		t.Fatalf("connect 1: %v", cErr)
	}
	if sErr := cs1.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); sErr != nil {
		t.Fatalf("Subscribe 1: %v", sErr)
	}
	drain(notif1)

	notif2 := make(chan string, 4)
	cs2, cErr := connectMCP(ctx, t, h.url, "alice-token", notif2)
	if cErr != nil {
		t.Fatalf("connect 2: %v", cErr)
	}
	if sErr := cs2.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); sErr != nil {
		t.Fatalf("Subscribe 2: %v", sErr)
	}
	drain(notif2)

	if _, err := h.tasksClient.UpdateTask(ctx, &tasksv1.UpdateTaskRequest{
		Id:    task.GetId(),
		Title: "renamed",
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	want := map[string]bool{"cs1": true, "cs2": true}
	deadline := time.After(2 * time.Second)
	for len(want) > 0 {
		select {
		case got := <-notif1:
			if got != uri {
				t.Errorf("cs1 notification = %q, want %q", got, uri)
			}
			delete(want, "cs1")
		case got := <-notif2:
			if got != uri {
				t.Errorf("cs2 notification = %q, want %q", got, uri)
			}
			delete(want, "cs2")
		case <-deadline:
			t.Fatalf("timed out; still waiting on sessions: %v", want)
		}
	}
}

// drain empties any buffered events from c. Used to discard
// pre-subscribe notifications so subsequent assertions are
// deterministic.
func drain(c <-chan string) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}
