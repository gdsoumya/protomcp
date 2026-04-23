package schema

import (
	"embed"
	"fmt"
	"reflect"
	"strings"
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
// silently succeed-but-vacuously, not actually asserting anything.
//
// Regenerate with:
//
//	cd internal/gen/schema/testdata
//	buf build --as-file-descriptor-set -o decorations.binpb --path decorations.proto
//
//go:embed testdata/decorations.binpb
var decoratedBin []byte

//go:embed testdata/decorations.binpb
var decoratedFS embed.FS

// badExamplesBin is a separate FileDescriptorSet carrying an intentionally
// malformed `@example` marker so TestExamplesInvalidJSON can assert that
// ForInputE surfaces a file:line-scoped codegen error.
//
//go:embed testdata/bad_examples.binpb
var badExamplesBin []byte

// loadDecorated parses decorations.binpb and returns the descriptor for the
// named message from the decorated test proto.
func loadDecorated(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	return loadDescriptor(t, decoratedBin, "decorations.binpb", name)
}

// loadDescriptor parses a FileDescriptorSet blob and returns the descriptor
// for the named message. Shared between loadDecorated and the bad-examples
// fixture loader so each embed uses a fresh protoregistry.Files and we do
// not accidentally pollute the global type registry.
func loadDescriptor(t *testing.T, bin []byte, label, name string) protoreflect.MessageDescriptor {
	t.Helper()
	_ = decoratedFS // silence unused on platforms that skip embed

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(bin, &fds); err != nil {
		t.Fatalf("unmarshal %s: %v", label, err)
	}
	// The FileDescriptorSet may contain transitively-imported files. Use a
	// fresh Files registry so we do not collide with the global one that
	// other tests populate via side-effect imports.
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		t.Fatalf("protodesc.NewFiles(%s): %v", label, err)
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
		t.Fatalf("message %q not found in %s", name, label)
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

// TestExamplesHappyPath verifies that a field with two `// @example` lines
// produces a two-entry `examples` array (in declaration order) and that the
// non-`@example` lines in the same comment block feed the `description`.
func TestExamplesHappyPath(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Examples")
	s := jsonRound(t, ForInput(md, Options{}))

	title := get(t, s, "properties", "title").(map[string]any)
	if got := title["description"]; got != "Human-readable task title." {
		t.Errorf("title.description: want %q, got %v", "Human-readable task title.", got)
	}
	ex, ok := title["examples"].([]any)
	if !ok {
		t.Fatalf("title.examples missing or wrong type: %T %v", title["examples"], title["examples"])
	}
	want := []any{"Buy milk", "Finish Q2 report"}
	if !reflect.DeepEqual(ex, want) {
		t.Errorf("title.examples: want %v, got %v", want, ex)
	}
}

// TestExamplesMixedTypes verifies that string / integer / bool / object
// payloads all round-trip through JSON correctly and end up in the
// `examples` array with their native JSON types preserved.
func TestExamplesMixedTypes(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Examples")
	s := jsonRound(t, ForInput(md, Options{}))

	// integer
	asn := get(t, s, "properties", "assignees").(map[string]any)
	asnEx := asn["examples"].([]any)
	if len(asnEx) != 1 || asnEx[0] != float64(3) { // JSON numbers decode to float64
		t.Errorf("assignees.examples: want [3], got %v", asnEx)
	}

	// boolean
	urg := get(t, s, "properties", "urgent").(map[string]any)
	urgEx := urg["examples"].([]any)
	if len(urgEx) != 1 || urgEx[0] != true {
		t.Errorf("urgent.examples: want [true], got %v", urgEx)
	}

	// nested object
	pay := get(t, s, "properties", "payload").(map[string]any)
	payEx := pay["examples"].([]any)
	if len(payEx) != 1 {
		t.Fatalf("payload.examples: want 1 entry, got %v", payEx)
	}
	obj, ok := payEx[0].(map[string]any)
	if !ok {
		t.Fatalf("payload.examples[0]: want object, got %T", payEx[0])
	}
	if obj["key"] != "value" || obj["n"] != float64(1) {
		t.Errorf("payload.examples[0]: want {key:value,n:1}, got %v", obj)
	}
}

// TestExamplesAbsent verifies that fields without any `@example` marker do
// not carry an `examples` key at all, the schema stays minimal.
func TestExamplesAbsent(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.Examples")
	s := jsonRound(t, ForInput(md, Options{}))

	plain := get(t, s, "properties", "plain").(map[string]any)
	if _, ok := plain["examples"]; ok {
		t.Errorf("plain.examples should be absent, got %v", plain["examples"])
	}
	// Sanity: a field from a completely different fixture with no
	// `@example` markers should also not grow an `examples` key.
	doc := loadDecorated(t, "protomcp.testdata.decorations.v1.Documented")
	ds := jsonRound(t, ForInput(doc, Options{}))
	if _, ok := ds["properties"].(map[string]any)["name"].(map[string]any)["examples"]; ok {
		t.Errorf("Documented.name.examples leaked")
	}
}

// TestExamplesInvalidJSON verifies that a malformed `@example` payload is
// surfaced through ForInputE as a *commentError carrying the proto file
// path, a non-zero line number, and the fully-qualified field name.
func TestExamplesInvalidJSON(t *testing.T) {
	md := loadDescriptor(t, badExamplesBin, "bad_examples.binpb",
		"protomcp.testdata.decorations.v1.BadExample")

	_, err := ForInputE(md, Options{})
	if err == nil {
		t.Fatal("ForInputE: expected error for invalid @example, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bad_examples.proto") {
		t.Errorf("error should mention the proto file path, got %q", msg)
	}
	if !strings.Contains(msg, "BadExample.title") {
		t.Errorf("error should mention the field FullName, got %q", msg)
	}
	if !strings.Contains(msg, "@example") {
		t.Errorf("error should mention the @example marker, got %q", msg)
	}
	// The file:line segment prepends the file, so "path:<digits>" should appear.
	if !strings.Contains(msg, "bad_examples.proto:") {
		t.Errorf("error should carry a file:line prefix, got %q", msg)
	}

	// ForInput (non-E) must panic on the same input, that's how the
	// generator surfaces the codegen failure.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ForInput: expected panic for invalid @example")
		}
	}()
	_ = ForInput(md, Options{})
}

// TestEnumDescriptionsHappyPath verifies that an enum whose values all carry
// `//` comments produces a parallel `enumDescriptions` array matching the
// `enum` array element-for-element.
func TestEnumDescriptionsHappyPath(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.EnumDecorations")
	s := jsonRound(t, ForInput(md, Options{}))

	status := get(t, s, "properties", "status").(map[string]any)
	got := status["enumDescriptions"].([]any)
	want := []any{
		"",
		"Task has not been started.",
		"Work is in progress.",
		"Task is complete.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status.enumDescriptions: want %v, got %v", want, got)
	}
	// Sanity: the `enum` array must still be present and aligned.
	enum := status["enum"].([]any)
	if len(enum) != len(got) {
		t.Errorf("enum/enumDescriptions length mismatch: %d vs %d", len(enum), len(got))
	}
}

// TestEnumDescriptionsSparse verifies that when only some enum values carry
// comments, every empty slot is preserved as "" so array positions stay
// aligned with the `enum` array.
func TestEnumDescriptionsSparse(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.EnumDecorations")
	s := jsonRound(t, ForInput(md, Options{}))

	pri := get(t, s, "properties", "priority").(map[string]any)
	got := pri["enumDescriptions"].([]any)
	want := []any{
		"", // PRIORITY_UNSPECIFIED
		"", // LOW
		"High-priority items are surfaced above everything else.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("priority.enumDescriptions: want %v, got %v", want, got)
	}
}

// TestEnumDescriptionsAllEmpty verifies that an enum whose values are all
// uncommented does NOT carry an `enumDescriptions` key (no array of empty
// strings).
func TestEnumDescriptionsAllEmpty(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.EnumDecorations")
	s := jsonRound(t, ForInput(md, Options{}))

	fl := get(t, s, "properties", "flavor").(map[string]any)
	if _, ok := fl["enumDescriptions"]; ok {
		t.Errorf("flavor.enumDescriptions should be absent, got %v", fl["enumDescriptions"])
	}
	// `enum` itself must still be present.
	if _, ok := fl["enum"]; !ok {
		t.Errorf("flavor.enum missing")
	}
}

// TestEnumLevelDescriptionFallback verifies that an enum field with no
// leading comment inherits its enum type's leading comment as the field's
// `description`.
func TestEnumLevelDescriptionFallback(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.EnumDecorations")
	s := jsonRound(t, ForInput(md, Options{}))

	// `status` field has no own leading comment → falls back to the
	// TaskStatus enum type's leading comment.
	status := get(t, s, "properties", "status").(map[string]any)
	if got := status["description"]; got != "Task progress state." {
		t.Errorf("status.description: want enum-type comment fallback %q, got %v",
			"Task progress state.", got)
	}
}

// TestEnumLevelDescriptionOverride verifies that when an enum field carries
// its own leading comment, that comment wins over the enum type's leading
// comment, field comment > enum-type comment > nothing.
func TestEnumLevelDescriptionOverride(t *testing.T) {
	md := loadDecorated(t, "protomcp.testdata.decorations.v1.EnumDecorations")
	s := jsonRound(t, ForInput(md, Options{}))

	field := get(t, s, "properties", "statusWithFieldComment").(map[string]any)
	got := fmt.Sprintf("%v", field["description"])
	// The field's own leading comment in decorations.proto spans two
	// lines; they're joined with a newline in the emitted description.
	const want = "Field with its own leading comment: description must use this, NOT the\nenum type's leading comment."
	if got != want {
		t.Errorf("statusWithFieldComment.description: want %q, got %q", want, got)
	}
}

// Silence unused-registry warnings in environments that tree-shake.
var _ = protoregistry.GlobalTypes
