// Package tasks_test drives the full CRUD surface of the tasks example
// through an in-memory MCP client, verifying:
//   - every annotated RPC appears as a tool with the correct hints
//   - OUTPUT_ONLY fields are stripped from the advertised input schema
//     but still present in responses
//   - CRUD round-trips (Create → List → Get → Update → Delete) work
//   - OUTPUT_ONLY values injected by a malicious client are dropped
//     by protomcp.ClearOutputOnly before the upstream gRPC call
//   - delete is idempotent (deleting the same id twice still succeeds)
package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// startGRPC boots the Tasks service on a random loopback port.
func startGRPC(t *testing.T) tasksv1.TasksClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	tasksv1.RegisterTasksServer(grpcSrv, tasksserver.New())
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return tasksv1.NewTasksClient(conn)
}

// connect wires an in-memory MCP client to srv and returns the session.
// The client is configured with an ElicitationHandler that auto-accepts ,
// the CRUD tests want to exercise the full round-trip and are not the
// place to assert elicitation behavior. Tests that need a different
// handler (e.g. decline) call connectWith directly.
func connect(ctx context.Context, t *testing.T, srv *protomcp.Server) *mcp.ClientSession {
	t.Helper()
	return connectWith(ctx, t, srv, &mcp.ClientOptions{
		ElicitationHandler: acceptElicitation,
	})
}

// connectWith is the general-purpose variant of connect, letting tests
// supply custom client options, most importantly, an ElicitationHandler
// that returns a specific action. The server side is identical; only the
// client's behavior differs between tests.
func connectWith(ctx context.Context, t *testing.T, srv *protomcp.Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, opts)
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
	return cs
}

// acceptElicitation is the default ElicitationHandler our CRUD tests use.
// Returning action=accept makes the handler a no-op from the caller's
// perspective: the gated tool runs as if the gate were never there.
func acceptElicitation(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	return &mcp.ElicitResult{Action: "accept"}, nil
}

// newServer builds a protomcp.Server with the Tasks tools registered and
// returns the pair (server, mcpClient). Used by every CRUD test.
func newServer(ctx context.Context, t *testing.T) *mcp.ClientSession {
	t.Helper()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	return connect(ctx, t, srv)
}

// callTool invokes a tool and unmarshals the text content into dst using
// protojson (which knows how to parse Timestamp, Duration, enums, etc.).
// It fails the test on transport error or an IsError response. When dst
// is nil the body is discarded.
func callTool(ctx context.Context, t *testing.T, cs *mcp.ClientSession, name, args string, dst proto.Message) {
	t.Helper()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("%s: call: %v", name, err)
	}
	if out.IsError {
		text := ""
		if len(out.Content) > 0 {
			if tc, ok := out.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("%s: IsError: %s", name, text)
	}
	if dst == nil {
		return
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if err := protojson.Unmarshal([]byte(text), dst); err != nil {
		t.Fatalf("%s: protojson.Unmarshal %q: %v", name, text, err)
	}
}

// TestToolsListHints verifies each CRUD RPC surfaces as a tool with the
// annotation-hints it declared in the proto. The LLM client uses these
// hints to decide retry / confirmation behavior.
func TestToolsListHints(t *testing.T) {
	ctx := context.Background()
	cs := newServer(ctx, t)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	for _, n := range []string{
		"Tasks_ListTasks", "Tasks_GetTask", "Tasks_CreateTask",
		"Tasks_UpdateTask", "Tasks_DeleteTask",
	} {
		if _, ok := byName[n]; !ok {
			t.Errorf("tool %s missing from tools/list", n)
		}
	}

	// Read-only hints on List + Get.
	for _, n := range []string{"Tasks_ListTasks", "Tasks_GetTask"} {
		if a := byName[n].Annotations; a == nil || !a.ReadOnlyHint {
			t.Errorf("%s: ReadOnlyHint = false, want true", n)
		}
	}
	// Create has no hints.
	if a := byName["Tasks_CreateTask"].Annotations; a != nil && (a.ReadOnlyHint || a.IdempotentHint) {
		t.Errorf("Tasks_CreateTask: unexpected hints set: %+v", a)
	}
	// Update is idempotent only.
	if a := byName["Tasks_UpdateTask"].Annotations; a == nil || !a.IdempotentHint {
		t.Errorf("Tasks_UpdateTask: IdempotentHint = false, want true")
	}
	// Delete is idempotent AND destructive.
	del := byName["Tasks_DeleteTask"].Annotations
	if del == nil || !del.IdempotentHint {
		t.Errorf("Tasks_DeleteTask: IdempotentHint = false, want true")
	}
	if del == nil || del.DestructiveHint == nil || !*del.DestructiveHint {
		t.Errorf("Tasks_DeleteTask: DestructiveHint = nil/false, want true (ptr)")
	}
}

// TestInputSchemaStripsOutputOnly, the advertised input schema must hide
// the OUTPUT_ONLY fields on Task (id, createdAt, updatedAt). This is the
// contract CreateTask relies on: a well-behaved client cannot supply
// server-computed values through the schema. (The runtime also scrubs
// them defensively; see TestCreateIgnoresClientSuppliedOutputOnly.)
func TestInputSchemaStripsOutputOnly(t *testing.T) {
	ctx := context.Background()
	cs := newServer(ctx, t)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var create *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "Tasks_CreateTask" {
			create = tool
		}
	}
	if create == nil || create.InputSchema == nil {
		t.Fatalf("Tasks_CreateTask missing or has no input schema")
	}

	raw, err := json.Marshal(create.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	s := string(raw)

	// Positive: title, description, done must appear on the embedded task.
	for _, want := range []string{`"title"`, `"description"`, `"done"`} {
		if !contains(s, want) {
			t.Errorf("input schema missing expected field %s: %s", want, s)
		}
	}
	// Negative: id, createdAt, updatedAt must not.
	for _, banned := range []string{`"id"`, `"createdAt"`, `"updatedAt"`} {
		if contains(s, banned) {
			t.Errorf("input schema leaks OUTPUT_ONLY field %s: %s", banned, s)
		}
	}
}

// TestCRUDRoundTrip exercises the full lifecycle: create, list, get,
// update, delete. Every step goes through the MCP client so the
// translation protojson ↔ proto is validated end-to-end.
func TestCRUDRoundTrip(t *testing.T) {
	ctx := context.Background()
	cs := newServer(ctx, t)

	// Create
	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask",
		`{"task":{"title":"buy milk","description":"2L","done":false}}`, &created)
	if created.Id == "" {
		t.Fatalf("Create: id empty")
	}
	if created.Title != "buy milk" {
		t.Errorf("Create: title = %q, want 'buy milk'", created.Title)
	}
	if created.CreatedAt == nil || created.UpdatedAt == nil {
		t.Errorf("Create: expected server-set timestamps, got createdAt=%v updatedAt=%v",
			created.CreatedAt, created.UpdatedAt)
	}

	// List, must contain the created task.
	var listed tasksv1.ListTasksResponse
	callTool(ctx, t, cs, "Tasks_ListTasks", `{}`, &listed)
	if len(listed.Tasks) != 1 || listed.Tasks[0].Id != created.Id {
		t.Errorf("List: got %+v, want single task with id %s", listed.Tasks, created.Id)
	}

	// Get
	var got tasksv1.Task
	callTool(ctx, t, cs, "Tasks_GetTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &got)
	if got.Id != created.Id || got.Title != "buy milk" {
		t.Errorf("Get: got %+v, want title='buy milk' id=%s", &got, created.Id)
	}

	// Update, flip done to true; title must stay set (REQUIRED).
	// UpdateTaskRequest uses flat fields (not an embedded Task) because
	// Task.id is OUTPUT_ONLY and would be stripped from the input schema.
	var updated tasksv1.Task
	callTool(ctx, t, cs, "Tasks_UpdateTask",
		fmt.Sprintf(`{"id":%q,"title":"buy milk","done":true}`, created.Id), &updated)
	if !updated.Done {
		t.Errorf("Update: Done = false after update to true")
	}
	if updated.Id != created.Id {
		t.Errorf("Update: id changed: %s -> %s", created.Id, updated.Id)
	}

	// Delete, first call returns existed=true.
	var del1 tasksv1.DeleteTaskResponse
	callTool(ctx, t, cs, "Tasks_DeleteTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &del1)
	if !del1.Existed {
		t.Errorf("Delete (first): Existed = false, want true")
	}

	// Delete idempotent, second call still succeeds, but existed=false.
	var del2 tasksv1.DeleteTaskResponse
	callTool(ctx, t, cs, "Tasks_DeleteTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &del2)
	if del2.Existed {
		t.Errorf("Delete (second): Existed = true, want false (idempotent)")
	}
}

// TestCreateIgnoresClientSuppliedOutputOnly, if a client bypasses the
// advertised input schema and sends id / createdAt directly, the runtime
// must clear them before the upstream gRPC server sees them. This is the
// defense-in-depth guarantee protomcp.ClearOutputOnly provides.
func TestCreateIgnoresClientSuppliedOutputOnly(t *testing.T) {
	ctx := context.Background()
	cs := newServer(ctx, t)

	// The JSON includes id + createdAt that a well-behaved client would
	// never send; the wire format accepts them, but our runtime strips them.
	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask", `{
		"task": {
			"id": "client-forged-id",
			"title": "sneak",
			"createdAt": "1970-01-01T00:00:00Z",
			"updatedAt": "1970-01-01T00:00:00Z"
		}
	}`, &created)

	if created.Id == "client-forged-id" {
		t.Errorf("Create used client-supplied id, OUTPUT_ONLY stripping failed")
	}
	if created.CreatedAt != nil && created.CreatedAt.Seconds == 0 {
		t.Errorf("Create used client-supplied createdAt (unix 0); got %v", created.CreatedAt)
	}
}

// TestGetNotFound verifies that a missing id surfaces through the MCP
// tool result as IsError=true (gRPC NOT_FOUND routed through the
// DefaultErrorHandler for non-auth codes).
func TestGetNotFound(t *testing.T) {
	ctx := context.Background()
	cs := newServer(ctx, t)

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Tasks_GetTask",
		Arguments: json.RawMessage(`{"id":"does-not-exist"}`),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.IsError {
		t.Errorf("expected IsError for missing id; got success: %+v", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
