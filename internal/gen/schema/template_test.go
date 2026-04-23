package schema

import (
	"strings"
	"testing"

	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
)

// TestParseMustache_RejectsSections, the generator's template
// language is deliberately logic-less. Parsing a template with a
// section, inverted section, or partial must error with a clear
// pointer at the offending tag.
func TestParseMustache_RejectsSections(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"section", `hello {{#items}}x{{/items}}`, "sections"},
		{"inverted", `hello {{^empty}}none{{/empty}}`, "inverted sections"},
		{"partial", `hello {{>header}}`, "partials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMustache(tc.src)
			if err == nil {
				t.Fatalf("expected error for %q", tc.src)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want substring %q", err, tc.want)
			}
		})
	}
}

// TestParseMustache_AcceptsVariables, plain {{var}} interpolation is
// the one supported form.
func TestParseMustache_AcceptsVariables(t *testing.T) {
	tmpl, err := ParseMustache(`title = {{title}}, desc = {{description}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vars := MustacheVariables(tmpl)
	if len(vars) != 2 || vars[0] != "title" || vars[1] != "description" {
		t.Errorf("vars = %v, want [title description]", vars)
	}
}

// TestValidateMustacheFieldPaths_OnTaskMessage, using the real Task
// message from examples/tasks makes sure JSONName resolution is
// exercised on a live descriptor.
func TestValidateMustacheFieldPaths_OnTaskMessage(t *testing.T) {
	md := (&tasksv1.Task{}).ProtoReflect().Descriptor()
	ok, err := ParseMustache(`{{title}} / {{description}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if vErr := ValidateMustacheFieldPaths(ok, md); vErr != nil {
		t.Errorf("expected clean validation, got %v", vErr)
	}

	bad, _ := ParseMustache(`{{nope}}`)
	err = ValidateMustacheFieldPaths(bad, md)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error naming the unresolved var, got %v", err)
	}
}

// TestValidatePlaceholderBindings, the URI template placeholders
// must match bindings exactly on both sides.
func TestValidatePlaceholderBindings(t *testing.T) {
	md := (&tasksv1.GetTaskRequest{}).ProtoReflect().Descriptor()

	// Happy path: one placeholder, one binding, field resolves.
	if err := ValidatePlaceholderBindings([]string{"id"}, map[string]string{"id": "id"}, md); err != nil {
		t.Errorf("expected clean, got %v", err)
	}

	// Missing binding.
	err := ValidatePlaceholderBindings([]string{"id"}, map[string]string{}, md)
	if err == nil || !strings.Contains(err.Error(), "missing bindings") {
		t.Errorf("expected missing-bindings error, got %v", err)
	}

	// Extra binding that doesn't appear in template.
	err = ValidatePlaceholderBindings([]string{"id"}, map[string]string{"id": "id", "extra": "id"}, md)
	if err == nil || !strings.Contains(err.Error(), "placeholders not in URI template") {
		t.Errorf("expected extra-binding error, got %v", err)
	}

	// Binding field doesn't resolve on the message.
	err = ValidatePlaceholderBindings([]string{"id"}, map[string]string{"id": "nope"}, md)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected path-resolution error, got %v", err)
	}
}

// TestResolveItemMessage, walks item_path and returns the element
// type of the terminal repeated field.
func TestResolveItemMessage(t *testing.T) {
	listMD := (&tasksv1.ListTasksResponse{}).ProtoReflect().Descriptor()
	itemMD, err := ResolveItemMessage(listMD, "tasks")
	if err != nil {
		t.Fatalf("resolve tasks: %v", err)
	}
	if got := string(itemMD.Name()); got != "Task" {
		t.Errorf("item type = %s, want Task", got)
	}

	// A scalar or missing path should error.
	if _, err := ResolveItemMessage(listMD, "missing"); err == nil {
		t.Errorf("expected error for missing path")
	}
}

// TestParseURITemplate_Errors, malformed templates surface the raw
// source in the error message so codegen diagnostics are actionable.
func TestParseURITemplate_Errors(t *testing.T) {
	_, _, err := ParseURITemplate("tasks://{id")
	if err == nil || !strings.Contains(err.Error(), "tasks://{id") {
		t.Errorf("err = %v; want raw template included", err)
	}
}
