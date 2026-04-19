package protomcp

import (
	"testing"
)

// TestMustParseSchemaValid verifies a well-formed JSON Schema is parsed
// into a non-nil *jsonschema.Schema with the expected top-level
// properties.
func TestMustParseSchemaValid(t *testing.T) {
	src := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"]
	}`
	got := MustParseSchema(src)
	if got == nil {
		t.Fatalf("expected non-nil schema")
	}
	if got.Type != "object" {
		t.Fatalf("Type = %q, want %q", got.Type, "object")
	}
	if len(got.Required) != 1 || got.Required[0] != "name" {
		t.Fatalf("Required = %v, want [name]", got.Required)
	}
	if _, ok := got.Properties["name"]; !ok {
		t.Fatalf("expected properties.name to be present")
	}
}

// TestMustParseSchemaPanicsOnInvalid verifies MustParseSchema panics
// when given non-JSON input, mirroring the contract for generated code.
func TestMustParseSchemaPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic, got none")
		}
	}()
	_ = MustParseSchema("this is not json")
}
