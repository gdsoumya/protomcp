package protomcp

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// MustParseSchema unmarshals src into a *jsonschema.Schema and panics
// on error. Intended for package-level init in generated code where
// src is a compile-time constant.
func MustParseSchema(src string) *jsonschema.Schema {
	s := &jsonschema.Schema{}
	if err := json.Unmarshal([]byte(src), s); err != nil {
		panic(fmt.Sprintf("protomcp: parse schema: %v", err))
	}
	return s
}

// BoolPtr returns a pointer to v.
func BoolPtr(v bool) *bool { return &v }
