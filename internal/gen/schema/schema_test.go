package schema

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/gdsoumya/protomcp/internal/gen/schema/testdata"
)

// descByName looks up a message descriptor registered in the global protoregistry.
// The testdata package is imported for its side effects (registration).
func descByName(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatalf("descriptor %s not found: %v", name, err)
	}
	return mt.Descriptor()
}

// jsonRound marshals a schema to JSON and back so we can compare via reflect.DeepEqual
// without worrying about map insertion order.
func jsonRound(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// get returns nested map keys as a single lookup, e.g. get(s, "properties", "i64", "type").
func get(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	cur := any(m)
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: expected map at %q, got %T", path, p, cur)
		}
		cur = mm[p]
	}
	return cur
}

func requiredSet(m map[string]any) map[string]bool {
	got := map[string]bool{}
	if r, ok := m["required"].([]any); ok {
		for _, v := range r {
			got[v.(string)] = true
		}
	}
	return got
}

// propertyNames returns the sorted set of property names on an object schema.
func propertyNames(m map[string]any) []string {
	props, _ := m["properties"].(map[string]any)
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestScalars(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Scalars")
	schema := jsonRound(t, ForInput(md, Options{}))

	// int64 and uint64 must render as string per protojson.
	if got := get(t, schema, "properties", "i64", "type"); got != "string" {
		t.Errorf("int64 type: want string, got %v", got)
	}
	if got := get(t, schema, "properties", "u64", "type"); got != "string" {
		t.Errorf("uint64 type: want string, got %v", got)
	}
	// int32/uint32 remain integer.
	if got := get(t, schema, "properties", "i32", "type"); got != "integer" {
		t.Errorf("int32 type: want integer, got %v", got)
	}
	// bytes carries contentEncoding: base64.
	if got := get(t, schema, "properties", "raw", "contentEncoding"); got != "base64" {
		t.Errorf("bytes contentEncoding: want base64, got %v", got)
	}
}

func TestEnumPlain(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Enums")
	schema := jsonRound(t, ForInput(md, Options{}))

	plain := get(t, schema, "properties", "plain").(map[string]any)
	if plain["type"] != "string" {
		t.Errorf("enum type: want string, got %v", plain["type"])
	}
	vals := plain["enum"].([]any)
	want := []string{"STATUS_UNSPECIFIED", "STATUS_OK", "STATUS_ERROR"}
	got := make([]string, len(vals))
	for i, v := range vals {
		got[i] = v.(string)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enum values: want %v, got %v", want, got)
	}
}

func TestEnumConstraints(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Enums")
	schema := jsonRound(t, ForInput(md, Options{}))

	// defined_only + UNSPECIFIED still allowed (we don't auto-exclude UNSPECIFIED).
	// Nothing to compare strictly, just ensure the enum was preserved.
	if _, ok := get(t, schema, "properties", "definedOnly").(map[string]any)["enum"]; !ok {
		t.Error("defined_only: expected enum values")
	}

	// in: [1, 2] → STATUS_OK, STATUS_ERROR
	onlyIn := get(t, schema, "properties", "onlyIn").(map[string]any)
	enumVals := onlyIn["enum"].([]any)
	if len(enumVals) != 2 || enumVals[0] != "STATUS_OK" || enumVals[1] != "STATUS_ERROR" {
		t.Errorf("only_in enum: got %v", enumVals)
	}

	// const: 1 → single-value enum
	constant := get(t, schema, "properties", "constant").(map[string]any)
	cVals := constant["enum"].([]any)
	if len(cVals) != 1 || cVals[0] != "STATUS_OK" {
		t.Errorf("constant enum: got %v", cVals)
	}
}

func TestRequiredAndOutputOnly(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Required")
	in := jsonRound(t, ForInput(md, Options{}))

	// OUTPUT_ONLY field must be absent from input properties.
	if _, ok := in["properties"].(map[string]any)["serverComputed"]; ok {
		t.Error("OUTPUT_ONLY field leaked into input schema")
	}
	// The other three fields must be present.
	if _, ok := in["properties"].(map[string]any)["apiRequired"]; !ok {
		t.Error("api_required missing from input")
	}

	req := requiredSet(in)
	if !req["apiRequired"] {
		t.Error("api_required not in required[]")
	}
	if !req["protovalidateRequired"] {
		t.Error("protovalidate_required not in required[]")
	}
	if req["optionalField"] {
		t.Error("optional_field wrongly listed as required")
	}

	// Output schema includes server_computed (no stripping).
	out := jsonRound(t, ForOutput(md, Options{}))
	if _, ok := out["properties"].(map[string]any)["serverComputed"]; !ok {
		t.Error("server_computed missing from output schema")
	}
}

func TestStringConstraints(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Strings")
	s := jsonRound(t, ForInput(md, Options{}))

	if f := get(t, s, "properties", "uuid", "format"); f != "uuid" {
		t.Errorf("uuid format: %v", f)
	}
	if f := get(t, s, "properties", "email", "format"); f != "email" {
		t.Errorf("email format: %v", f)
	}
	if p := get(t, s, "properties", "pat", "pattern"); p != "^[a-z]+$" {
		t.Errorf("pattern: %v", p)
	}
	if mn := get(t, s, "properties", "ranged", "minLength"); mn != float64(3) {
		t.Errorf("minLength: %v (%T)", mn, mn)
	}
	if mx := get(t, s, "properties", "ranged", "maxLength"); mx != float64(10) {
		t.Errorf("maxLength: %v", mx)
	}
}

func TestNumericConstraints(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Numeric")
	s := jsonRound(t, ForInput(md, Options{}))

	// gt/lt → exclusiveMinimum/exclusiveMaximum
	if v := get(t, s, "properties", "i32Open", "exclusiveMinimum"); v != float64(0) {
		t.Errorf("i32_open exclusiveMinimum: %v", v)
	}
	if v := get(t, s, "properties", "i32Open", "exclusiveMaximum"); v != float64(100) {
		t.Errorf("i32_open exclusiveMaximum: %v", v)
	}
	// gte/lte → minimum/maximum
	if v := get(t, s, "properties", "i32Closed", "minimum"); v != float64(0) {
		t.Errorf("i32_closed minimum: %v", v)
	}
	if v := get(t, s, "properties", "i32Closed", "maximum"); v != float64(100) {
		t.Errorf("i32_closed maximum: %v", v)
	}
	// float closed
	if v := get(t, s, "properties", "fClosed", "maximum"); v != float64(1) {
		t.Errorf("f_closed maximum: %v", v)
	}
	// double open → exclusive bounds
	if v := get(t, s, "properties", "dOpen", "exclusiveMinimum"); v != float64(0) {
		t.Errorf("d_open exclusiveMinimum: %v", v)
	}
	if v := get(t, s, "properties", "dOpen", "exclusiveMaximum"); v != float64(1) {
		t.Errorf("d_open exclusiveMaximum: %v", v)
	}
}

func TestRepeatedRules(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Listy")
	s := jsonRound(t, ForInput(md, Options{}))

	tags := get(t, s, "properties", "tags").(map[string]any)
	if tags["type"] != "array" {
		t.Errorf("tags.type: %v", tags["type"])
	}
	if tags["minItems"] != float64(1) {
		t.Errorf("tags.minItems: %v", tags["minItems"])
	}
	if tags["maxItems"] != float64(5) {
		t.Errorf("tags.maxItems: %v", tags["maxItems"])
	}
	if tags["uniqueItems"] != true {
		t.Errorf("tags.uniqueItems: %v", tags["uniqueItems"])
	}

	// Item-level constraints reach through GetRepeated().GetItems().
	bounded := get(t, s, "properties", "boundedItems").(map[string]any)
	items := bounded["items"].(map[string]any)
	if items["minLength"] != float64(2) {
		t.Errorf("bounded_items item minLength: %v", items["minLength"])
	}
}

func TestMapConstraints(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Mapy")
	s := jsonRound(t, ForInput(md, Options{}))

	counts := get(t, s, "properties", "counts").(map[string]any)
	if counts["type"] != "object" {
		t.Errorf("counts.type: %v", counts["type"])
	}
	pn := counts["propertyNames"].(map[string]any)
	if pn["minLength"] != float64(1) {
		t.Errorf("counts key minLength: %v", pn["minLength"])
	}
	ap := counts["additionalProperties"].(map[string]any)
	if ap["minimum"] != float64(0) {
		t.Errorf("counts value minimum: %v", ap["minimum"])
	}

	// int64 map key → numeric pattern on propertyNames.
	byID := get(t, s, "properties", "byId").(map[string]any)
	keyConstraints := byID["propertyNames"].(map[string]any)
	if _, ok := keyConstraints["pattern"]; !ok {
		t.Errorf("int64 map key: expected pattern, got %v", keyConstraints)
	}
}

func TestWellKnownTypes(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.WellKnown")
	s := jsonRound(t, ForInput(md, Options{}))

	cases := map[string]struct {
		want map[string]any
	}{
		"ts":  {map[string]any{"format": "date-time"}},
		"dur": {map[string]any{"pattern": `^-?[0-9]+(\.[0-9]+)?s$`}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			prop := get(t, s, "properties", name).(map[string]any)
			for k, v := range tc.want {
				if prop[k] != v {
					t.Errorf("%s[%s]: want %v, got %v", name, k, v, prop[k])
				}
			}
		})
	}

	// Wrapper types: nullable primitive.
	sv := get(t, s, "properties", "sv").(map[string]any)
	types := sv["type"].([]any)
	if len(types) != 2 || types[0] != "string" || types[1] != "null" {
		t.Errorf("StringValue type: want [string null], got %v", types)
	}
	// Int64Value renders as nullable string (not number).
	lv := get(t, s, "properties", "i64v").(map[string]any)
	lvTypes := lv["type"].([]any)
	if lvTypes[0] != "string" {
		t.Errorf("Int64Value primary type: want string, got %v", lvTypes[0])
	}
}

func TestOneofs(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Oneofs")
	s := jsonRound(t, ForInput(md, Options{}))

	// Two real oneofs (choice, fallback) produce two anyOf entries in declaration order.
	anyOf, ok := s["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("expected two anyOf entries (choice, fallback), got %d: %v", len(anyOf), s["anyOf"])
	}
	choiceOneOf := anyOf[0].(map[string]any)["oneOf"].([]any)
	if len(choiceOneOf) != 4 {
		t.Errorf("choice oneOf: want 4 branches (text,count,nested,label), got %d", len(choiceOneOf))
	}
	fallbackOneOf := anyOf[1].(map[string]any)["oneOf"].([]any)
	if len(fallbackOneOf) != 2 {
		t.Errorf("fallback oneOf: want 2 branches (reason,silent), got %d", len(fallbackOneOf))
	}

	// Synthetic oneofs (proto3 `optional`) must NOT appear in anyOf; the fields
	// should be regular optional properties.
	props := s["properties"].(map[string]any)
	for _, name := range []string{"maybeNote", "maybeCount"} {
		if _, ok := props[name]; !ok {
			t.Errorf("%s should be a regular optional property (synthetic oneof)", name)
		}
		if requiredSet(s)[name] {
			t.Errorf("%s wrongly required", name)
		}
	}
}

func TestRecursionCap(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Recursive")
	s := jsonRound(t, ForInput(md, Options{MaxRecursionDepth: 2}))

	// First expansion has "name" and "child".
	if names := propertyNames(s); !reflect.DeepEqual(names, []string{"child", "name"}) {
		t.Fatalf("top-level properties: %v", names)
	}
	// Second level expansion has "name" and "child" (placeholder).
	child1 := get(t, s, "properties", "child").(map[string]any)
	if names := propertyNames(child1); !reflect.DeepEqual(names, []string{"child", "name"}) {
		t.Fatalf("child1 properties: %v", names)
	}
	// Third level = placeholder string with the JSON-encoded hint.
	child2 := get(t, child1, "properties", "child").(map[string]any)
	if child2["type"] != "string" {
		t.Errorf("recursion cap: expected string placeholder at max depth, got %v", child2["type"])
	}
}

func TestCleanComment(t *testing.T) {
	in := "  buf:lint: ignore_unused\n  Real description.\n  @ignore-comment\n  Another line."
	got := CleanComment(in)
	want := "Real description.\nAnother line."
	if got != want {
		t.Errorf("CleanComment: want %q, got %q", want, got)
	}
}

// Sanity: ensure ForInput output is JSON-serializable.
func TestSchemaIsJSONSerializable(t *testing.T) {
	for _, name := range []string{"Scalars", "Enums", "Required", "Strings", "Numeric", "Listy", "Mapy", "WellKnown", "Oneofs", "Recursive"} {
		t.Run(name, func(t *testing.T) {
			md := descByName(t, "protomcp.testdata.v1."+name)
			s := ForInput(md, Options{})
			if _, err := json.Marshal(s); err != nil {
				t.Fatalf("marshal: %v", err)
			}
		})
	}
	_ = proto.Message(nil) // silence unused import if it happens
}
