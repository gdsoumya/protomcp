package protomcp

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// MustParseSchema unmarshals a JSON Schema string into a
// *jsonschema.Schema and panics if the input is not valid JSON or does
// not match the Schema structure. It is intended for package-level
// variable initialization in generated code, where the schema source
// is a compile-time constant produced by the protoc plugin and any
// failure represents a generator bug rather than a runtime condition.
func MustParseSchema(src string) *jsonschema.Schema {
	s := &jsonschema.Schema{}
	if err := json.Unmarshal([]byte(src), s); err != nil {
		panic(fmt.Sprintf("protomcp: parse schema: %v", err))
	}
	return s
}

// BoolPtr returns a pointer to v. It exists so generated code can set
// *bool fields on mcp.ToolAnnotations (DestructiveHint) without
// inlining an anonymous closure at every call site.
func BoolPtr(v bool) *bool { return &v }
