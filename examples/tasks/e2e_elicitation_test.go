// Package tasks_test also covers the elicitation-gated DeleteTask path.
// DeleteTask carries both the destructive tool hint and a confirmation
// elicitation in tasks.proto; the generated handler must:
//
//   - fire session.Elicit before the upstream gRPC call
//   - render {{id}} into the prompt so the user sees *which* task
//   - run the gRPC call only when action=="accept"
//   - return an IsError CallToolResult with a clear message when the
//     user declines, without having touched the backend
//
// The server-side tasks.Delete is idempotent and stateful, so "decline
// then re-issue with accept" is a valid assertion: the second call
// must see the task because the first call short-circuited.
package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestDeleteTask_ElicitationAccept exercises the full confirm-then-delete
// path: the elicitation fires (carrying the rendered prompt), the client
// returns action=accept, and the backend observes the Delete call.
func TestDeleteTask_ElicitationAccept(t *testing.T) {
	ctx := context.Background()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)

	var seenMessage atomic.Value // string
	var elicitCalls atomic.Int32
	cs := connectWith(ctx, t, srv, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			elicitCalls.Add(1)
			if req != nil && req.Params != nil {
				seenMessage.Store(req.Params.Message)
			}
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})

	// Seed a task so Delete has something to find. Use the public CRUD
	// surface so the test path matches what a real client does.
	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask",
		`{"task":{"title":"delete-me","done":false}}`, &created)
	if created.Id == "" {
		t.Fatalf("Create: empty id")
	}

	// Delete, elicitation handler must fire before the backend sees the
	// call, and the rendered prompt must contain the task id.
	var del tasksv1.DeleteTaskResponse
	callTool(ctx, t, cs, "Tasks_DeleteTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &del)
	if !del.Existed {
		t.Errorf("Delete: Existed = false, want true (task was present before the delete)")
	}
	if got := elicitCalls.Load(); got != 1 {
		t.Errorf("elicitation handler called %d times, want 1", got)
	}
	msg, _ := seenMessage.Load().(string)
	if !strings.Contains(msg, created.Id) {
		t.Errorf("elicitation message %q does not contain task id %q", msg, created.Id)
	}
	if !strings.Contains(msg, "Delete task with id") {
		t.Errorf("elicitation message %q is missing the confirmation prefix", msg)
	}

	// Task is really gone, a second Delete returns existed=false.
	var del2 tasksv1.DeleteTaskResponse
	callTool(ctx, t, cs, "Tasks_DeleteTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &del2)
	if del2.Existed {
		t.Errorf("second Delete: Existed = true, want false (task was already removed)")
	}
}

// TestDeleteTask_ElicitationDecline asserts that when the client returns
// action=decline the gRPC Delete does NOT run: the task survives, and the
// tool result is an IsError with the canned decline message.
func TestDeleteTask_ElicitationDecline(t *testing.T) {
	ctx := context.Background()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)

	var elicitCalls atomic.Int32
	cs := connectWith(ctx, t, srv, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			elicitCalls.Add(1)
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
	})

	// Seed a task.
	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask",
		`{"task":{"title":"keep-me","done":false}}`, &created)

	// Call Delete, declined elicitation must surface as an IsError
	// response, not a transport-level error, and the backend must not
	// have seen the call.
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Tasks_DeleteTask",
		Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.Id)),
	})
	if err != nil {
		t.Fatalf("CallTool: transport error: %v", err)
	}
	if !out.IsError {
		t.Fatalf("Delete: want IsError, got success: %+v", out)
	}
	if got := elicitCalls.Load(); got != 1 {
		t.Errorf("elicitation handler called %d times, want 1", got)
	}
	if len(out.Content) == 0 {
		t.Fatalf("Delete: IsError result has no content")
	}
	text, _ := out.Content[0].(*mcp.TextContent)
	if text == nil {
		t.Fatalf("Delete: first content block is not TextContent: %T", out.Content[0])
	}
	if !strings.Contains(text.Text, "declined") {
		t.Errorf("Delete: decline message %q does not mention decline", text.Text)
	}

	// Task is still there, confirm via Get.
	var got tasksv1.Task
	callTool(ctx, t, cs, "Tasks_GetTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &got)
	if got.Id != created.Id {
		t.Errorf("Get after declined delete: got %q, want %q", got.Id, created.Id)
	}
	if got.Title != "keep-me" {
		t.Errorf("Get after declined delete: title=%q, want 'keep-me'", got.Title)
	}
}

// TestDeleteTask_ElicitationCancel asserts that action=cancel behaves the
// same as decline, it is a non-accept action, so the tool must
// short-circuit with IsError and leave the backend untouched.
func TestDeleteTask_ElicitationCancel(t *testing.T) {
	ctx := context.Background()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)

	cs := connectWith(ctx, t, srv, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "cancel"}, nil
		},
	})

	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask",
		`{"task":{"title":"keep-me","done":false}}`, &created)

	out, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "Tasks_DeleteTask",
		Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.Id)),
	})
	if err != nil {
		t.Fatalf("CallTool: transport error: %v", err)
	}
	if !out.IsError {
		t.Fatalf("Delete: want IsError on cancel, got success: %+v", out)
	}

	// Task survived.
	var got tasksv1.Task
	callTool(ctx, t, cs, "Tasks_GetTask",
		fmt.Sprintf(`{"id":%q}`, created.Id), &got)
	if got.Id != created.Id {
		t.Errorf("Get after canceled delete: got %q, want %q", got.Id, created.Id)
	}
}
