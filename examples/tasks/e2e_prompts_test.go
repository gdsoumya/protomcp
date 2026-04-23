// Package tasks_test ‑ prompt coverage for the Tasks service. The tool
// surface has its own e2e suite (e2e_test.go); this file drives the
// prompts/list and prompts/get flows end-to-end through an in-memory MCP
// client, plus the Mustache-template rendering path.
package tasks_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tasksserver "github.com/gdsoumya/protomcp/examples/tasks/server"
	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newPromptServer boots the Tasks gRPC service and wires both the tool
// and prompt register functions onto a single protomcp.Server, returning
// a connected in-memory MCP client session.
func newPromptServer(ctx context.Context, t *testing.T) *mcp.ClientSession {
	t.Helper()
	grpcClient := startGRPC(t)
	srv := protomcp.New("tasks", "0.1.0")
	tasksv1.RegisterTasksMCPTools(srv, grpcClient)
	tasksv1.RegisterTasksMCPPrompts(srv, grpcClient)
	_ = tasksserver.New // keep the import live for Grep-based tooling
	return connect(ctx, t, srv)
}

// TestPromptsList asserts tasks_review appears in prompts/list with the
// expected arguments (id required, no others).
func TestPromptsList(t *testing.T) {
	ctx := context.Background()
	cs := newPromptServer(ctx, t)

	list, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	var found *mcp.Prompt
	for _, p := range list.Prompts {
		if p.Name == "tasks_review" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatalf("tasks_review missing from prompts/list; got %d prompts", len(list.Prompts))
	}
	if found.Title != "Review a task" {
		t.Errorf("Title = %q, want %q", found.Title, "Review a task")
	}
	if len(found.Arguments) != 1 {
		t.Fatalf("Arguments length = %d, want 1", len(found.Arguments))
	}
	arg := found.Arguments[0]
	if arg.Name != "id" {
		t.Errorf("argument name = %q, want %q", arg.Name, "id")
	}
	if !arg.Required {
		t.Errorf("argument id Required = false, want true")
	}
}

// TestPromptsGetRendersTask exercises the full prompts/get path: create a
// task through the tool surface, then call prompts/get with its id and
// assert the rendered Mustache body contains every variable the template
// declared.
func TestPromptsGetRendersTask(t *testing.T) {
	ctx := context.Background()
	cs := newPromptServer(ctx, t)

	// Seed via the tool to exercise the real gRPC path. The returned
	// Task carries the server-assigned id used below.
	var created tasksv1.Task
	callTool(ctx, t, cs, "Tasks_CreateTask",
		`{"task":{"title":"triage prs","description":"weekly review","done":false}}`, &created)
	if created.Id == "" {
		t.Fatalf("Create returned empty id")
	}

	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "tasks_review",
		Arguments: map[string]string{"id": created.Id},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Messages length = %d, want 1", len(res.Messages))
	}
	msg := res.Messages[0]
	if msg.Role != "user" {
		t.Errorf("Role = %q, want user", msg.Role)
	}
	text, ok := msg.Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content is not TextContent: %T", msg.Content)
	}
	body := text.Text
	// Every template variable must appear rendered in the body.
	wantSubstrings := []string{
		"Review task triage prs",
		fmt.Sprintf("ID: %s", created.Id),
		"done=false",
		"Description: weekly review",
		"What should happen next?",
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(body, w) {
			t.Errorf("rendered body missing %q;\n--- body ---\n%s", w, body)
		}
	}
}

// TestPromptsGetNotFound verifies that a missing id surfaces as an error
// on the prompts/get call. The gRPC NOT_FOUND propagates through the
// DefaultPromptErrorHandler as a plain error (prompts have no IsError
// shape in the MCP spec).
func TestPromptsGetNotFound(t *testing.T) {
	ctx := context.Background()
	cs := newPromptServer(ctx, t)
	_, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "tasks_review",
		Arguments: map[string]string{"id": "does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected error on missing id; got nil")
	}
}
