// Package schema generates JSON-Schema-compatible map structures from
// protobuf message descriptors. The output is meant to be marshaled to
// JSON and then unmarshaled into github.com/google/jsonschema-go's Schema
// type for use as an MCP tool's InputSchema / OutputSchema.
package schema

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Options controls schema generation.
type Options struct {
	// MaxRecursionDepth caps how many times a recursive message type can be
	// expanded along a single path before being replaced with a string
	// placeholder. Zero uses the default (3).
	MaxRecursionDepth int
}

const defaultMaxRecursionDepth = 3

// fieldFilter decides whether a field should be included in the generated schema.
// It is used to implement input-only filtering (OUTPUT_ONLY stripping).
type fieldFilter func(protoreflect.FieldDescriptor) bool

// ForInput returns the JSON Schema map for use as an MCP tool's InputSchema.
// Fields annotated google.api.field_behavior = OUTPUT_ONLY are omitted.
func ForInput(md protoreflect.MessageDescriptor, opts Options) map[string]any {
	return messageSchema(md, opts, nil, isInputField)
}

// ForOutput returns the JSON Schema map for use as an MCP tool's OutputSchema.
// All fields are included.
func ForOutput(md protoreflect.MessageDescriptor, opts Options) map[string]any {
	return messageSchema(md, opts, nil, includeAllFields)
}

func includeAllFields(_ protoreflect.FieldDescriptor) bool { return true }

// isInputField reports whether a field should appear in an input schema.
// Fields marked OUTPUT_ONLY (AIP-203) are populated by the server and must
// not appear in client-supplied input.
func isInputField(fd protoreflect.FieldDescriptor) bool {
	if !proto.HasExtension(fd.Options(), annotations.E_FieldBehavior) {
		return true
	}
	behaviors, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	return !slices.Contains(behaviors, annotations.FieldBehavior_OUTPUT_ONLY)
}

// messageSchema is the internal recursive walker. seen tracks expansion depth
// per message FullName on the current path so we break cycles deterministically.
func messageSchema(
	md protoreflect.MessageDescriptor,
	opts Options,
	seen map[protoreflect.FullName]int,
	filter fieldFilter,
) map[string]any {
	if seen == nil {
		seen = make(map[protoreflect.FullName]int)
	}
	maxDepth := opts.MaxRecursionDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxRecursionDepth
	}
	if seen[md.FullName()] >= maxDepth {
		return map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("JSON-encoded %s. Provide a JSON object as a string.", md.Name()),
		}
	}
	seen[md.FullName()]++
	defer func() { seen[md.FullName()]-- }()

	required := []string{}
	properties := map[string]any{}
	oneOfGroups := map[string][]map[string]any{}

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !filter(fd) {
			continue
		}
		// We emit the protojson JSON name (camelCase by default, or the
		// field's json_name override) rather than the raw proto snake_case
		// name so that the schema matches what protojson.Marshal produces on
		// the response side. protojson accepts both on input, so LLMs fed
		// our schema can also produce the proto-name form and still parse.
		name := fd.JSONName()

		// Real (non-synthetic) oneofs are modeled as anyOf { oneOf: ... }.
		// Synthetic oneofs (proto3 `optional`) are ignored here and the field
		// is emitted as an ordinary optional field.
		if oneof := fd.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			key := string(oneof.Name())
			oneOfGroups[key] = append(oneOfGroups[key], map[string]any{
				"properties": map[string]any{name: fieldSchema(fd, opts, seen, filter)},
				"required":   []string{name},
			})
			continue
		}

		properties[name] = fieldSchema(fd, opts, seen, filter)
		if isRequired(fd) {
			required = append(required, name)
		}
	}

	// Iterate oneofs in declaration order so output is deterministic.
	var anyOf []map[string]any
	oneofs := md.Oneofs()
	for i := range oneofs.Len() {
		oo := oneofs.Get(i)
		if oo.IsSynthetic() {
			continue
		}
		if entries, ok := oneOfGroups[string(oo.Name())]; ok {
			anyOf = append(anyOf, map[string]any{"oneOf": entries})
		}
	}

	result := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		result["required"] = required
	}
	if anyOf != nil {
		result["anyOf"] = anyOf
	}
	return result
}

// fieldSchema builds the schema for a single field, including list/map wrapping
// and buf.validate constraint overlay.
func fieldSchema(
	fd protoreflect.FieldDescriptor,
	opts Options,
	seen map[protoreflect.FullName]int,
	filter fieldFilter,
) map[string]any {
	if fd.IsMap() {
		m := mapFieldSchema(fd, opts, seen, filter)
		decorateFieldSchema(fd, m)
		return m
	}

	var schema map[string]any
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		schema = messageFieldSchema(fd, opts, seen, filter)
	case protoreflect.EnumKind:
		schema = enumFieldSchema(fd)
	default:
		schema = scalarFieldSchema(fd)
	}

	maps.Copy(schema, extractValidateConstraints(fd))

	// Wrap in array if the field is repeated. Both the array wrapper and the
	// inner item schema receive field-level decorations (description,
	// deprecated marker) so the `description` renders on whichever shape the
	// caller inspects. Repeated-specific rules (minItems etc.) go on the
	// wrapper only.
	if fd.IsList() {
		list := map[string]any{"type": "array", "items": schema}
		applyRepeatedRules(fd, list)
		decorateFieldSchema(fd, list)
		return list
	}
	decorateFieldSchema(fd, schema)
	return schema
}

// decorateFieldSchema applies field-level JSON Schema decorations that are
// independent of the field's type:
//   - `description` from leading/trailing proto comments (when the descriptor
//     carries source info; runtime protoregistry does not, so this is a no-op
//     in that path),
//   - `deprecated: true` when the field has `[deprecated = true]`.
//
// We never overwrite an existing `description` — constraint mappers (e.g.,
// Any → `{@type, value}`) may have already set one that is more useful.
func decorateFieldSchema(fd protoreflect.FieldDescriptor, s map[string]any) {
	if _, has := s["description"]; !has {
		if desc := fieldDescription(fd); desc != "" {
			s["description"] = desc
		}
	}
	if fieldIsDeprecated(fd) {
		s["deprecated"] = true
	}
}

// fieldDescription returns the cleaned leading proto comment for fd, or the
// trailing comment if there is no leading one. Empty if the descriptor
// source file did not carry source_code_info (the common case when types
// were registered from compiled Go stubs via the runtime protoregistry).
func fieldDescription(fd protoreflect.FieldDescriptor) string {
	loc := fd.ParentFile().SourceLocations().ByDescriptor(fd)
	c := strings.TrimSpace(CleanComment(loc.LeadingComments))
	if c == "" {
		c = strings.TrimSpace(CleanComment(loc.TrailingComments))
	}
	return c
}

// fieldIsDeprecated reports whether the field carries `[deprecated = true]`.
// We intentionally do not inherit deprecation from the enclosing message —
// that would be surprising, and proto lets you mark either one independently.
func fieldIsDeprecated(fd protoreflect.FieldDescriptor) bool {
	opts := fd.Options()
	if opts == nil {
		return false
	}
	if fo, ok := opts.(*descriptorpb.FieldOptions); ok && fo != nil {
		return fo.GetDeprecated()
	}
	return false
}

// applyRepeatedRules reads buf.validate repeated-specific rules (min_items,
// max_items, unique) and applies them to the array schema. Element-level
// rules (e.g., repeated.items.string.pattern) are handled by extractValidateConstraints
// when it walks into GetRepeated().GetItems().
func applyRepeatedRules(fd protoreflect.FieldDescriptor, list map[string]any) {
	rules := fieldRules(fd)
	if rules == nil {
		return
	}
	rep := rules.GetRepeated()
	if rep == nil {
		return
	}
	if rep.HasMinItems() {
		list["minItems"] = rep.GetMinItems()
	}
	if rep.HasMaxItems() {
		list["maxItems"] = rep.GetMaxItems()
	}
	if rep.GetUnique() {
		list["uniqueItems"] = true
	}
}

// mapFieldSchema renders a proto map as a JSON object with propertyNames
// constraining the key format.
func mapFieldSchema(
	fd protoreflect.FieldDescriptor,
	opts Options,
	seen map[protoreflect.FullName]int,
	filter fieldFilter,
) map[string]any {
	keySchema := keyConstraints(fd.MapKey())
	valueSchema := fieldSchema(fd.MapValue(), opts, seen, filter)

	// buf.validate map.keys / map.values overlays
	if rules := fieldRules(fd); rules != nil {
		if m := rules.GetMap(); m != nil {
			overlay(keySchema, convertFieldRules(m.GetKeys()))
			overlay(valueSchema, convertFieldRules(m.GetValues()))
		}
	}

	out := map[string]any{
		"type":                 "object",
		"propertyNames":        keySchema,
		"additionalProperties": valueSchema,
	}
	return out
}

// keyConstraints returns a JSON Schema describing the stringified form of
// a map's key. Protojson keys are always strings; we add pattern/enum checks
// so LLMs don't hand us "abc" for an int key.
func keyConstraints(mk protoreflect.FieldDescriptor) map[string]any {
	s := map[string]any{"type": "string"}
	switch mk.Kind() {
	case protoreflect.BoolKind:
		s["enum"] = []string{"true", "false"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		s["pattern"] = `^(0|[1-9]\d*)$`
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		s["pattern"] = `^-?(0|[1-9]\d*)$`
	}
	return s
}

// messageFieldSchema handles nested messages, including well-known types
// that render as primitives in protojson.
func messageFieldSchema(
	fd protoreflect.FieldDescriptor,
	opts Options,
	seen map[protoreflect.FullName]int,
	filter fieldFilter,
) map[string]any {
	switch string(fd.Message().FullName()) {
	case "google.protobuf.Timestamp":
		return map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{"type": []string{"string", "null"}, "pattern": `^-?[0-9]+(\.[0-9]+)?s$`}
	case "google.protobuf.Struct":
		return map[string]any{"type": "object", "additionalProperties": true}
	case "google.protobuf.Value":
		return map[string]any{"description": "Any JSON value."}
	case "google.protobuf.ListValue":
		return map[string]any{"type": "array", "items": map[string]any{}}
	case "google.protobuf.FieldMask":
		return map[string]any{"type": "string"}
	case "google.protobuf.Any":
		// protojson serializes Any as an open object with an @type URL
		// plus the packed message's own fields at the top level (or, for
		// well-known-type payloads, a single "value" field). We can't
		// know the inner type at codegen time, so we emit a permissive
		// open object that accepts both shapes and requires only @type.
		return map[string]any{
			"type": []string{"object", "null"},
			"properties": map[string]any{
				"@type": map[string]any{"type": "string"},
			},
			"required":             []string{"@type"},
			"additionalProperties": true,
		}
	case "google.protobuf.DoubleValue", "google.protobuf.FloatValue",
		"google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		return map[string]any{"type": []string{"number", "null"}}
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return map[string]any{"type": []string{"string", "null"}}
	case "google.protobuf.StringValue":
		return map[string]any{"type": []string{"string", "null"}}
	case "google.protobuf.BoolValue":
		return map[string]any{"type": []string{"boolean", "null"}}
	case "google.protobuf.BytesValue":
		return map[string]any{"type": []string{"string", "null"}, "contentEncoding": "base64"}
	case "google.protobuf.Empty":
		return map[string]any{"type": "object"}
	default:
		return messageSchema(fd.Message(), opts, seen, filter)
	}
}

// enumFieldSchema emits string enum values using proto value names.
// If buf.validate constrains the set, the schema is narrowed accordingly.
func enumFieldSchema(fd protoreflect.FieldDescriptor) map[string]any {
	values := fd.Enum().Values()

	// buf.validate enum rules
	rules := fieldRules(fd)
	if rules != nil && rules.GetEnum() != nil {
		er := rules.GetEnum()
		if er.HasConst() {
			if ev := values.ByNumber(protoreflect.EnumNumber(er.GetConst())); ev != nil {
				return map[string]any{"type": "string", "enum": []string{string(ev.Name())}}
			}
		}
		if len(er.GetIn()) > 0 {
			names := make([]string, 0, len(er.GetIn()))
			for _, n := range er.GetIn() {
				if ev := values.ByNumber(protoreflect.EnumNumber(n)); ev != nil {
					names = append(names, string(ev.Name()))
				}
			}
			return map[string]any{"type": "string", "enum": names}
		}
		if er.GetDefinedOnly() || len(er.GetNotIn()) > 0 {
			excluded := make(map[int32]struct{}, len(er.GetNotIn()))
			for _, n := range er.GetNotIn() {
				excluded[n] = struct{}{}
			}
			names := make([]string, 0, values.Len())
			for i := range values.Len() {
				v := values.Get(i)
				if _, skip := excluded[int32(v.Number())]; skip {
					continue
				}
				names = append(names, string(v.Name()))
			}
			return map[string]any{"type": "string", "enum": names}
		}
	}

	names := make([]string, 0, values.Len())
	for i := range values.Len() {
		names = append(names, string(values.Get(i).Name()))
	}
	return map[string]any{"type": "string", "enum": names}
}

func scalarFieldSchema(fd protoreflect.FieldDescriptor) map[string]any {
	s := map[string]any{"type": kindToJSONType(fd.Kind())}
	if fd.Kind() == protoreflect.BytesKind {
		s["contentEncoding"] = "base64"
		s["format"] = "byte"
	}
	return s
}

// kindToJSONType maps a proto Kind to the JSON Schema type string used by
// protojson. Crucially int64/uint64/sint64/fixed64 render as JSON strings
// to avoid precision loss in JavaScript-based MCP clients.
func kindToJSONType(k protoreflect.Kind) string {
	switch k {
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "integer"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "string"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	case protoreflect.BytesKind, protoreflect.EnumKind:
		return "string"
	default:
		return "string"
	}
}

// isRequired reports whether a field should appear in the parent message's
// "required" array. Required-ness is an API concern, not a wire-format
// concern, so we read it only from explicit annotations:
//   - google.api.field_behavior = REQUIRED (AIP-203)
//   - buf.validate.field.required = true
func isRequired(fd protoreflect.FieldDescriptor) bool {
	if proto.HasExtension(fd.Options(), annotations.E_FieldBehavior) {
		behaviors, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
		if slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED) {
			return true
		}
	}
	if rules := fieldRules(fd); rules != nil && rules.GetRequired() {
		return true
	}
	return false
}

// fieldRules returns the buf.validate FieldRules attached to fd, or nil.
func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	if !proto.HasExtension(fd.Options(), validate.E_Field) {
		return nil
	}
	rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
	return rules
}

// extractValidateConstraints translates buf.validate scalar/bytes/string
// rules on fd into JSON-Schema keywords. Element-level rules on repeated
// fields are picked up here too because buf.validate nests them under
// FieldRules.GetRepeated().GetItems().
func extractValidateConstraints(fd protoreflect.FieldDescriptor) map[string]any {
	out := map[string]any{}
	rules := fieldRules(fd)
	if rules == nil {
		return out
	}

	// For repeated fields, per-item constraints live under GetRepeated().GetItems().
	if fd.IsList() {
		if items := rules.GetRepeated().GetItems(); items != nil {
			rules = items
		}
	}

	overlay(out, convertFieldRules(rules))
	return out
}

// convertFieldRules is the bulk of the buf.validate → JSON Schema mapping.
func convertFieldRules(rules *validate.FieldRules) map[string]any {
	out := map[string]any{}
	if rules == nil {
		return out
	}

	if b := rules.GetBool(); b != nil {
		if b.HasConst() {
			out["const"] = b.GetConst()
		}
	}

	if s := rules.GetString(); s != nil {
		applyStringRules(s, out)
	}

	if b := rules.GetBytes(); b != nil {
		applyBytesRules(b, out)
	}

	if r := rules.GetInt32(); r != nil {
		applyInt32Rules(r, out)
	}
	if r := rules.GetInt64(); r != nil {
		applyInt64Rules(r, out)
	}
	if r := rules.GetUint32(); r != nil {
		applyUint32Rules(r, out)
	}
	if r := rules.GetUint64(); r != nil {
		applyUint64Rules(r, out)
	}
	if r := rules.GetFloat(); r != nil {
		applyFloatRules(r, out)
	}
	if r := rules.GetDouble(); r != nil {
		applyDoubleRules(r, out)
	}

	return out
}

// applyStringRules covers every buf.validate.StringRules variant that has
// a sensible JSON Schema expression. Unknown / non-standard `format`
// strings (uri-reference, ipv4-with-prefixlen, etc.) are valid per JSON
// Schema draft 2020-12 — strict validators ignore them, LLM clients
// receive them as strong hints about the expected shape.
func applyStringRules(s *validate.StringRules, out map[string]any) {
	// Well-known string formats. Later entries would win, so we only set
	// a format if none has been set yet — buf.validate itself disallows
	// setting multiple at once (they live in a oneof), so at most one
	// will ever be truthy. The names follow JSON Schema draft 2020-12
	// where standardized, and buf.validate rule names otherwise.
	switch {
	case s.GetUuid():
		out["format"] = "uuid"
	case s.GetTuuid():
		// Trimmed UUID (no hyphens). No standard JSON Schema name; we
		// forward buf.validate's own for LLM guidance.
		out["format"] = "tuuid"
	case s.GetEmail():
		out["format"] = "email"
	case s.GetHostname():
		out["format"] = "hostname"
	case s.GetIp():
		out["format"] = "ip"
	case s.GetIpv4():
		out["format"] = "ipv4"
	case s.GetIpv6():
		out["format"] = "ipv6"
	case s.GetUri():
		out["format"] = "uri"
	case s.GetUriRef():
		out["format"] = "uri-reference"
	case s.GetAddress():
		out["format"] = "address" // hostname or IP
	case s.GetHostAndPort():
		out["format"] = "host-and-port"
	case s.GetIpWithPrefixlen():
		out["format"] = "ip-with-prefixlen"
	case s.GetIpv4WithPrefixlen():
		out["format"] = "ipv4-with-prefixlen"
	case s.GetIpv6WithPrefixlen():
		out["format"] = "ipv6-with-prefixlen"
	case s.GetIpPrefix():
		out["format"] = "ip-prefix"
	case s.GetIpv4Prefix():
		out["format"] = "ipv4-prefix"
	case s.GetIpv6Prefix():
		out["format"] = "ipv6-prefix"
	}

	if p := s.GetPattern(); p != "" {
		out["pattern"] = p
	}
	if s.HasConst() {
		out["const"] = s.GetConst()
	}
	if s.HasLen() {
		out["minLength"] = s.GetLen()
		out["maxLength"] = s.GetLen()
	}
	if s.HasMinLen() {
		out["minLength"] = s.GetMinLen()
	}
	if s.HasMaxLen() {
		out["maxLength"] = s.GetMaxLen()
	}
	if len(s.GetIn()) > 0 {
		out["enum"] = append([]string(nil), s.GetIn()...)
	}
	if len(s.GetNotIn()) > 0 {
		// JSON Schema has no "not-enum"; express as `not: {enum: [...]}`.
		out["not"] = map[string]any{"enum": append([]string(nil), s.GetNotIn()...)}
	}
}

// applyBytesRules maps bytes rules. protojson serializes bytes as
// base64 strings, so minLength/maxLength here are advisory — they
// constrain the base64 string length, not the raw byte count.
// protovalidate on the gRPC side remains authoritative.
func applyBytesRules(b *validate.BytesRules, out map[string]any) {
	if b.HasLen() {
		out["minLength"] = b.GetLen()
		out["maxLength"] = b.GetLen()
	}
	if b.HasMinLen() {
		out["minLength"] = b.GetMinLen()
	}
	if b.HasMaxLen() {
		out["maxLength"] = b.GetMaxLen()
	}
}

// The Int32/UInt32/Int64/UInt64/Float/Double rule helpers are
// structurally identical but strongly typed against distinct
// protovalidate rule messages. Generic-izing them would require
// parametric constraints across six unrelated proto types with
// different getter signatures (int32 vs uint32 vs int64 …) and the
// resulting code would be harder to read than six short functions.
// Keep them explicit.
//
//nolint:dupl // see block comment above
func applyInt32Rules(r *validate.Int32Rules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = int(r.GetGt())
	} else if r.HasGte() {
		out["minimum"] = int(r.GetGte())
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = int(r.GetLt())
	} else if r.HasLte() {
		out["maximum"] = int(r.GetLte())
	}
	if r.HasConst() {
		out["const"] = int(r.GetConst())
	}
	if in := r.GetIn(); len(in) > 0 {
		out["enum"] = append([]int32(nil), in...)
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		out["not"] = map[string]any{"enum": append([]int32(nil), nin...)}
	}
}

// Int64 fields render as JSON strings per protojson (protobuf convention
// to preserve precision beyond 2^53 in JavaScript clients), so the
// numeric bounds and the {const, enum, not-enum} values emitted below
// all live on a "type": "string" schema. Strict JSON Schema validators
// treat numeric keywords on string-typed values as a no-op, so these
// are guidance for LLMs (they signal the intended range / allowed
// values) rather than enforced constraints. protovalidate on the gRPC
// side is the authoritative check. Same reasoning applies to
// applyUint64Rules.
func applyInt64Rules(r *validate.Int64Rules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = r.GetGt()
	} else if r.HasGte() {
		out["minimum"] = r.GetGte()
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = r.GetLt()
	} else if r.HasLte() {
		out["maximum"] = r.GetLte()
	}
	if r.HasConst() {
		out["const"] = r.GetConst()
	}
	if in := r.GetIn(); len(in) > 0 {
		out["enum"] = append([]int64(nil), in...)
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		out["not"] = map[string]any{"enum": append([]int64(nil), nin...)}
	}
}

//nolint:dupl // see comment on applyInt32Rules
func applyUint32Rules(r *validate.UInt32Rules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = int(r.GetGt())
	} else if r.HasGte() {
		out["minimum"] = int(r.GetGte())
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = int(r.GetLt())
	} else if r.HasLte() {
		out["maximum"] = int(r.GetLte())
	}
	if r.HasConst() {
		out["const"] = int(r.GetConst())
	}
	if in := r.GetIn(); len(in) > 0 {
		out["enum"] = append([]uint32(nil), in...)
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		out["not"] = map[string]any{"enum": append([]uint32(nil), nin...)}
	}
}

func applyUint64Rules(r *validate.UInt64Rules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = r.GetGt()
	} else if r.HasGte() {
		out["minimum"] = r.GetGte()
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = r.GetLt()
	} else if r.HasLte() {
		out["maximum"] = r.GetLte()
	}
	if r.HasConst() {
		out["const"] = r.GetConst()
	}
	if in := r.GetIn(); len(in) > 0 {
		out["enum"] = append([]uint64(nil), in...)
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		out["not"] = map[string]any{"enum": append([]uint64(nil), nin...)}
	}
}

func applyFloatRules(r *validate.FloatRules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = float64(r.GetGt())
	} else if r.HasGte() {
		out["minimum"] = float64(r.GetGte())
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = float64(r.GetLt())
	} else if r.HasLte() {
		out["maximum"] = float64(r.GetLte())
	}
	if r.HasConst() {
		out["const"] = float64(r.GetConst())
	}
	if in := r.GetIn(); len(in) > 0 {
		vals := make([]float64, len(in))
		for i, v := range in {
			vals[i] = float64(v)
		}
		out["enum"] = vals
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		vals := make([]float64, len(nin))
		for i, v := range nin {
			vals[i] = float64(v)
		}
		out["not"] = map[string]any{"enum": vals}
	}
}

func applyDoubleRules(r *validate.DoubleRules, out map[string]any) {
	if r.HasGt() {
		out["exclusiveMinimum"] = r.GetGt()
	} else if r.HasGte() {
		out["minimum"] = r.GetGte()
	}
	if r.HasLt() {
		out["exclusiveMaximum"] = r.GetLt()
	} else if r.HasLte() {
		out["maximum"] = r.GetLte()
	}
	if r.HasConst() {
		out["const"] = r.GetConst()
	}
	if in := r.GetIn(); len(in) > 0 {
		out["enum"] = append([]float64(nil), in...)
	}
	if nin := r.GetNotIn(); len(nin) > 0 {
		out["not"] = map[string]any{"enum": append([]float64(nil), nin...)}
	}
}

func overlay(dst, src map[string]any) {
	maps.Copy(dst, src)
}

// CleanComment strips tool-specific comment prefixes (buf:lint, @ignore-comment)
// and normalizes whitespace so the remaining text is suitable as a JSON Schema
// "description".
func CleanComment(comment string) string {
	var out []string
	prefixes := []string{"buf:lint:", "@ignore-comment"}
outer:
	for line := range strings.SplitSeq(comment, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				continue outer
			}
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}
