package schema

import (
	"embed"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// decoratedBin is a FileDescriptorSet compiled with --include_source_info so
// field-level comments survive into the descriptors. Runtime protoregistry
// strips source info, so without this embed the description tests would
// silently succeed-but-vacuously — not actually asserting anything.
//
// Generated with:
//
//	protoc --include_source_info --include_imports --descriptor_set_out=decorations.binpb \
//	    -I testdata decorations.proto
//
//go:embed testdata/decorations.binpb
var decoratedBin []byte

//go:embed testdata/decorations.binpb
var decoratedFS embed.FS

// loadDecorated parses decorated.binpb and returns the descriptor for the
// named message from the decorated test proto.
func loadDecorated(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	_ = decoratedFS // silence unused on platforms that skip embed

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(decoratedBin, &fds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The FileDescriptorSet may contain transitively-imported files. Use a
	// fresh Files registry so we do not collide with the global one that
	// other tests populate via side-effect imports.
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	var md protoreflect.MessageDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := range msgs.Len() {
			m := msgs.Get(i)
			if string(m.FullName()) == name {
				md = m
				return false
			}
		}
		return true
	})
	if md == nil {
		t.Fatalf("message %q not found in decorations.binpb", name)
	}
	return md
}

// TestFieldDescriptionFromComment verifies that a field's leading proto
// comment becomes the JSON Schema `description` on the property.
func TestFieldDescriptionFromComment(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Documented")
	s := jsonRound(t, ForInput(md, Options{}))

	wantDescriptions := map[string]string{
		"name":  "The user's display name.",
		"email": "Primary contact email. Must be verifiable.",
	}
	for field, want := range wantDescriptions {
		got := get(t, s, "properties", field, "description")
		if fmt.Sprintf("%v", got) != want {
			t.Errorf("%s.description: want %q, got %q", field, want, got)
		}
	}
}

// TestDeprecatedMarker verifies [deprecated = true] surfaces as
// "deprecated": true in the JSON Schema.
func TestDeprecatedMarker(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Documented")
	s := jsonRound(t, ForInput(md, Options{}))

	if v := get(t, s, "properties", "legacyId", "deprecated"); v != true {
		t.Errorf("legacyId.deprecated: want true, got %v", v)
	}
	// Non-deprecated fields must NOT have the key (presence is the signal).
	if v := get(t, s, "properties", "name", "deprecated"); v != nil {
		t.Errorf("name.deprecated: want absent, got %v", v)
	}
}

// TestJSONNames verifies the schema uses the protojson JSON name (camelCase)
// rather than the proto field name (snake_case).
func TestJSONNames(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Documented")
	s := jsonRound(t, ForInput(md, Options{}))

	props := s["properties"].(map[string]any)
	for _, want := range []string{"name", "email", "legacyId", "contactInfo"} {
		if _, ok := props[want]; !ok {
			t.Errorf("JSON-name property %q missing; keys=%v", want, keys(props))
		}
	}
	for _, notWant := range []string{"legacy_id", "contact_info"} {
		if _, ok := props[notWant]; ok {
			t.Errorf("proto-name property %q leaked into schema", notWant)
		}
	}

	// required must also use JSON names.
	req := requiredSet(s)
	if !req["name"] {
		t.Errorf("name missing from required[]")
	}
	if req["legacy_id"] {
		t.Errorf("proto-name 'legacy_id' leaked into required[]")
	}
}

// TestDescriptionOnRepeated asserts that repeated-field descriptions land on
// the array wrapper (not only the items), which is how LLMs read them.
func TestDescriptionOnRepeated(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Documented")
	s := jsonRound(t, ForInput(md, Options{}))

	d := get(t, s, "properties", "tags", "description")
	if fmt.Sprintf("%v", d) != "Free-form tags." {
		t.Errorf("tags.description: %v", d)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Silence unused-registry warnings in environments that tree-shake.
var _ = protoregistry.GlobalTypes
