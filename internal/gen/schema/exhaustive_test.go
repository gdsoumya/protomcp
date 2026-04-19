package schema

import (
	"encoding/json"
	"testing"
)

// TestScalarsExhaustive asserts every proto scalar Kind maps to the correct
// JSON type, with int64-family kinds rendered as "string" per protojson.
func TestScalarsExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Scalars")
	s := jsonRound(t, ForInput(md, Options{}))

	want := map[string]string{
		"s":     "string",
		"b":     "boolean",
		"i32":   "integer",
		"i64":   "string",
		"u32":   "integer",
		"u64":   "string",
		"si32":  "integer",
		"si64":  "string",
		"fx32":  "integer",
		"fx64":  "string",
		"sfx32": "integer",
		"sfx64": "string",
		"f":     "number",
		"d":     "number",
		"raw":   "string",
	}
	for name, wantType := range want {
		t.Run(name, func(t *testing.T) {
			got := get(t, s, "properties", name, "type")
			if got != wantType {
				t.Errorf("%s: want type %q, got %v", name, wantType, got)
			}
		})
	}

	// bytes gets format + contentEncoding; 64-bit ints do not.
	raw := get(t, s, "properties", "raw").(map[string]any)
	if raw["contentEncoding"] != "base64" {
		t.Errorf("raw.contentEncoding: %v", raw["contentEncoding"])
	}
	if raw["format"] != "byte" {
		t.Errorf("raw.format: %v", raw["format"])
	}
	// int64 must not carry pattern/format bytes.
	i64 := get(t, s, "properties", "i64").(map[string]any)
	if _, has := i64["contentEncoding"]; has {
		t.Errorf("i64 should not have contentEncoding")
	}
}

// TestEnumsExhaustive exercises every buf.validate enum constraint variant
// and the plain (constraint-less) path.
func TestEnumsExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Enums")
	s := jsonRound(t, ForInput(md, Options{}))

	cases := map[string][]string{
		"plain":       {"STATUS_UNSPECIFIED", "STATUS_OK", "STATUS_ERROR"},
		"definedOnly": {"STATUS_UNSPECIFIED", "STATUS_OK", "STATUS_ERROR"}, // defined_only allows all declared values
		"onlyIn":      {"STATUS_OK", "STATUS_ERROR"},
		"constant":    {"STATUS_OK"},
		"notIn":       {"STATUS_OK", "STATUS_ERROR"}, // excludes STATUS_UNSPECIFIED (value 0)
	}
	for field, wantEnum := range cases {
		t.Run(field, func(t *testing.T) {
			got := get(t, s, "properties", field, "enum").([]any)
			if len(got) != len(wantEnum) {
				t.Fatalf("enum values: want %v, got %v", wantEnum, got)
			}
			for i, w := range wantEnum {
				if got[i] != w {
					t.Errorf("enum[%d]: want %q, got %v", i, w, got[i])
				}
			}
			// Every enum field must type:string.
			if get(t, s, "properties", field, "type") != "string" {
				t.Errorf("%s.type: want string", field)
			}
		})
	}
}

// TestRequiredExhaustive exercises input/output stripping with REQUIRED
// and OUTPUT_ONLY, including the "both" edge case.
func TestRequiredExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Required")

	// Input: server_computed and both stripped; api_required and
	// protovalidate_required are required[]; optional_field is neither.
	in := jsonRound(t, ForInput(md, Options{}))
	inProps := in["properties"].(map[string]any)

	mustHave := []string{"apiRequired", "protovalidateRequired", "optionalField"}
	mustNotHave := []string{"serverComputed", "both"}
	for _, n := range mustHave {
		if _, ok := inProps[n]; !ok {
			t.Errorf("input should contain %q", n)
		}
	}
	for _, n := range mustNotHave {
		if _, ok := inProps[n]; ok {
			t.Errorf("input should NOT contain OUTPUT_ONLY %q", n)
		}
	}

	req := requiredSet(in)
	if !req["apiRequired"] {
		t.Error("api_required missing from required[]")
	}
	if !req["protovalidateRequired"] {
		t.Error("protovalidate_required missing from required[]")
	}
	if req["optionalField"] {
		t.Error("optional_field wrongly required")
	}

	// Output: every field present (OUTPUT_ONLY only strips on input).
	out := jsonRound(t, ForOutput(md, Options{}))
	outProps := out["properties"].(map[string]any)
	for _, n := range append(mustHave, mustNotHave...) {
		if _, ok := outProps[n]; !ok {
			t.Errorf("output should contain %q", n)
		}
	}
}

// TestStringsExhaustive covers every buf.validate string format/pattern
// and range rule.
func TestStringsExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Strings")
	s := jsonRound(t, ForInput(md, Options{}))

	cases := []struct {
		field, key string
		want       any
	}{
		{"uuid", "format", "uuid"},
		{"email", "format", "email"},
		{"ipv4", "format", "ipv4"},
		{"ipv6", "format", "ipv6"},
		{"hostname", "format", "hostname"},
		{"uri", "format", "uri"},
		{"pat", "pattern", "^[a-z]+$"},
		{"ranged", "minLength", float64(3)},
		{"ranged", "maxLength", float64(10)},
	}
	for _, c := range cases {
		t.Run(c.field+"_"+c.key, func(t *testing.T) {
			got := get(t, s, "properties", c.field, c.key)
			if got != c.want {
				t.Errorf("want %v, got %v", c.want, got)
			}
		})
	}

	// in: → enum on the field
	inEnum := get(t, s, "properties", "onlyIn", "enum").([]any)
	if len(inEnum) != 2 || inEnum[0] != "alpha" || inEnum[1] != "beta" {
		t.Errorf("only_in enum: got %v", inEnum)
	}
}

// TestNumericExhaustive covers every numeric rule variant.
func TestNumericExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Numeric")
	s := jsonRound(t, ForInput(md, Options{}))

	// gt/lt → exclusive bounds
	if v := get(t, s, "properties", "i32Open", "exclusiveMinimum"); v != float64(0) {
		t.Errorf("i32_open exclusiveMinimum: %v", v)
	}
	// gte/lte → inclusive bounds
	if v := get(t, s, "properties", "i32Closed", "minimum"); v != float64(0) {
		t.Errorf("i32_closed minimum: %v", v)
	}
	// const
	if v := get(t, s, "properties", "i32Const", "const"); v != float64(42) {
		t.Errorf("i32_const const: %v", v)
	}
	// in → enum
	inVals := get(t, s, "properties", "i32In", "enum").([]any)
	if len(inVals) != 3 {
		t.Errorf("i32_in enum len: %d", len(inVals))
	}
	// int64 bounds rendered as JSON numbers even though the type is "string".
	if v := get(t, s, "properties", "i64Ranged", "minimum"); v != float64(1) {
		t.Errorf("i64_ranged minimum: %v", v)
	}
	// uint32 rules
	if v := get(t, s, "properties", "u32Ranged", "maximum"); v != float64(255) {
		t.Errorf("u32_ranged maximum: %v", v)
	}
	// uint64 rules
	if v := get(t, s, "properties", "u64Ranged", "maximum"); v != float64(9999999999) {
		t.Errorf("u64_ranged maximum: %v", v)
	}
}

// TestListyExhaustive covers repeated scalars, messages, enums, bytes.
func TestListyExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Listy")
	s := jsonRound(t, ForInput(md, Options{}))

	// repeated scalar with repeated rules.
	tags := get(t, s, "properties", "tags").(map[string]any)
	if tags["type"] != "array" || tags["minItems"] != float64(1) || tags["uniqueItems"] != true {
		t.Errorf("tags: %v", tags)
	}

	// item-level constraints reach through GetRepeated().GetItems()
	bi := get(t, s, "properties", "boundedItems").(map[string]any)
	items := bi["items"].(map[string]any)
	if items["minLength"] != float64(2) {
		t.Errorf("bounded item minLength: %v", items["minLength"])
	}

	// repeated messages
	nm := get(t, s, "properties", "nestedMessages").(map[string]any)
	if nm["type"] != "array" {
		t.Errorf("nested_messages type: %v", nm["type"])
	}
	nmItems := nm["items"].(map[string]any)
	if nmItems["type"] != "object" {
		t.Errorf("nested_messages.items.type: %v", nmItems["type"])
	}
	// The nested message schema must carry its own properties.
	if _, ok := nmItems["properties"]; !ok {
		t.Errorf("nested_messages.items.properties missing")
	}

	// repeated enums → array of strings
	st := get(t, s, "properties", "statuses").(map[string]any)
	stItems := st["items"].(map[string]any)
	if stItems["type"] != "string" {
		t.Errorf("statuses.items.type: %v", stItems["type"])
	}

	// repeated bytes → array of base64 strings
	bl := get(t, s, "properties", "blobs").(map[string]any)
	blItems := bl["items"].(map[string]any)
	if blItems["contentEncoding"] != "base64" {
		t.Errorf("blobs.items.contentEncoding: %v", blItems["contentEncoding"])
	}
}

// TestMapyExhaustive covers every map key variant (string, int32, int64,
// uint32, bool) and every value shape (scalar, message, enum).
func TestMapyExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Mapy")
	s := jsonRound(t, ForInput(md, Options{}))

	// Top-level shape: every map is an object with propertyNames + additionalProperties.
	for _, field := range []string{"counts", "byId", "byI32", "byU32ToEnum", "byBool"} {
		m := get(t, s, "properties", field).(map[string]any)
		if m["type"] != "object" {
			t.Errorf("%s type: %v", field, m["type"])
		}
		if _, ok := m["propertyNames"]; !ok {
			t.Errorf("%s missing propertyNames", field)
		}
		if _, ok := m["additionalProperties"]; !ok {
			t.Errorf("%s missing additionalProperties", field)
		}
	}

	// bool key → enum
	bk := get(t, s, "properties", "byBool", "propertyNames").(map[string]any)
	boolEnum := bk["enum"].([]any)
	if len(boolEnum) != 2 {
		t.Errorf("bool key enum: %v", boolEnum)
	}

	// int64 key → pattern with sign
	ik := get(t, s, "properties", "byId", "propertyNames").(map[string]any)
	if p, _ := ik["pattern"].(string); p == "" || p[0] != '^' {
		t.Errorf("int64 key pattern: %v", ik["pattern"])
	}

	// uint32 key → unsigned pattern
	uk := get(t, s, "properties", "byU32ToEnum", "propertyNames").(map[string]any)
	if p, _ := uk["pattern"].(string); p == "" {
		t.Errorf("uint32 key pattern missing")
	}

	// message value (Scalars) → object with properties
	iv := get(t, s, "properties", "byI32", "additionalProperties").(map[string]any)
	if iv["type"] != "object" {
		t.Errorf("by_i32 value type: %v", iv["type"])
	}

	// enum value (Status) → string with enum
	ev := get(t, s, "properties", "byU32ToEnum", "additionalProperties").(map[string]any)
	if ev["type"] != "string" {
		t.Errorf("by_u32_to_enum value type: %v", ev["type"])
	}
	if _, ok := ev["enum"]; !ok {
		t.Errorf("by_u32_to_enum value missing enum")
	}
}

// TestBytesyExhaustive covers buf.validate.bytes rules.
func TestBytesyExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Bytesy")
	s := jsonRound(t, ForInput(md, Options{}))

	if v := get(t, s, "properties", "bounded", "minLength"); v != float64(1) {
		t.Errorf("bounded minLength: %v", v)
	}
	if v := get(t, s, "properties", "bounded", "maxLength"); v != float64(1024) {
		t.Errorf("bounded maxLength: %v", v)
	}
	if v := get(t, s, "properties", "unbounded", "contentEncoding"); v != "base64" {
		t.Errorf("unbounded contentEncoding: %v", v)
	}
}

// TestWellKnownExhaustive exercises every well-known type we support.
func TestWellKnownExhaustive(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.WellKnown")
	s := jsonRound(t, ForInput(md, Options{}))

	// Primitive-like WKTs
	ts := get(t, s, "properties", "ts").(map[string]any)
	if ts["format"] != "date-time" {
		t.Errorf("Timestamp.format: %v", ts["format"])
	}
	dur := get(t, s, "properties", "dur").(map[string]any)
	if _, ok := dur["pattern"]; !ok {
		t.Errorf("Duration.pattern missing")
	}
	mask := get(t, s, "properties", "mask").(map[string]any)
	if mask["type"] != "string" {
		t.Errorf("FieldMask.type: %v", mask["type"])
	}
	empty := get(t, s, "properties", "empty").(map[string]any)
	if empty["type"] != "object" {
		t.Errorf("Empty.type: %v", empty["type"])
	}

	// Struct is an open object
	obj := get(t, s, "properties", "obj").(map[string]any)
	if obj["type"] != "object" {
		t.Errorf("Struct.type: %v", obj["type"])
	}
	if obj["additionalProperties"] != true {
		t.Errorf("Struct.additionalProperties: %v", obj["additionalProperties"])
	}

	// ListValue is an array of anything
	lst := get(t, s, "properties", "lst").(map[string]any)
	if lst["type"] != "array" {
		t.Errorf("ListValue.type: %v", lst["type"])
	}

	// Any has @type
	anyMsg := get(t, s, "properties", "pkt").(map[string]any)
	anyProps := anyMsg["properties"].(map[string]any)
	if _, ok := anyProps["@type"]; !ok {
		t.Errorf("Any missing @type property")
	}

	// Wrapper types nullable
	wrappers := map[string][]string{
		"sv":     {"string", "null"},
		"i32v":   {"number", "null"},
		"i64v":   {"string", "null"},
		"u32v":   {"number", "null"},
		"u64v":   {"string", "null"},
		"bv":     {"boolean", "null"},
		"dv":     {"number", "null"},
		"fv":     {"number", "null"},
		"bytesv": {"string", "null"},
	}
	for name, wantTypes := range wrappers {
		t.Run(name, func(t *testing.T) {
			types := get(t, s, "properties", name, "type").([]any)
			if len(types) != 2 || types[0] != wantTypes[0] || types[1] != wantTypes[1] {
				t.Errorf("%s type: want %v, got %v", name, wantTypes, types)
			}
		})
	}
}

// TestDeepNesting verifies 3+ levels of nested message schemas render.
func TestDeepNesting(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Deep")
	s := jsonRound(t, ForInput(md, Options{}))

	// L1 → L2 → L3 → leaf
	leaf := get(t, s, "properties", "l1", "properties", "l2", "properties", "l3", "properties", "leaf", "type")
	if leaf != "string" {
		t.Errorf("deep.l1.l2.l3.leaf.type: want string, got %v", leaf)
	}
}

// TestMutualRecursion covers A↔B mutual cycle detection.
func TestMutualRecursion(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.MutualA")

	// With a depth of 2 the recursion should terminate without panicking and
	// produce a string placeholder somewhere in the tree.
	out := ForInput(md, Options{MaxRecursionDepth: 2})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonContains(raw, `"JSON-encoded`) {
		t.Errorf("expected recursion placeholder somewhere in output; got %s", raw)
	}
}

func jsonContains(b []byte, needle string) bool {
	return indexBytes(b, needle) >= 0
}

func indexBytes(b []byte, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(b); i++ {
		if string(b[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}

// TestInputVsOutputDivergence verifies ForInput and ForOutput produce
// materially different schemas for messages with OUTPUT_ONLY fields.
func TestInputVsOutputDivergence(t *testing.T) {
	md := descByName(t, "protomcp.testdata.v1.Required")
	in := ForInput(md, Options{})
	out := ForOutput(md, Options{})

	inProps := in["properties"].(map[string]any)
	outProps := out["properties"].(map[string]any)
	if len(outProps) <= len(inProps) {
		t.Errorf("output schema should have >= fields than input; in=%d out=%d", len(inProps), len(outProps))
	}
	if _, ok := inProps["serverComputed"]; ok {
		t.Errorf("input should not contain server_computed")
	}
	if _, ok := outProps["serverComputed"]; !ok {
		t.Errorf("output should contain server_computed")
	}
}

// TestSchemaDeterminism asserts running ForInput twice on the same
// descriptor yields byte-identical JSON output.
func TestSchemaDeterminism(t *testing.T) {
	names := []string{"Scalars", "Enums", "Required", "Strings", "Numeric", "Listy", "Mapy", "WellKnown", "Oneofs", "Recursive", "Bytesy", "Deep", "MutualA"}
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			md := descByName(t, "protomcp.testdata.v1."+n)
			a, _ := json.Marshal(ForInput(md, Options{}))
			b, _ := json.Marshal(ForInput(md, Options{}))
			if string(a) != string(b) {
				t.Errorf("non-deterministic schema for %s:\n  %s\n  %s", n, a, b)
			}
		})
	}
}
