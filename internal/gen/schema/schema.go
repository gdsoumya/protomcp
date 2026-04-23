// Package schema generates JSON-Schema-compatible map structures from
// protobuf message descriptors. The output is meant to be marshaled to
// JSON and then unmarshaled into github.com/google/jsonschema-go's Schema
// type for use as an MCP tool's InputSchema / OutputSchema.
package schema

import (
	"encoding/json"
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
	// MaxRecursionDepth caps recursive message expansion per path
	// before substituting a string placeholder. Zero uses the default
	// (3).
	MaxRecursionDepth int
}

const defaultMaxRecursionDepth = 3

// fieldFilter decides whether a field appears in the schema.
type fieldFilter func(protoreflect.FieldDescriptor) bool

// ForInput returns the JSON Schema map for use as an MCP tool's InputSchema.
// Fields annotated google.api.field_behavior = OUTPUT_ONLY are omitted.
//
// Panics if a field carries an invalid `// @example <json>` marker; callers
// that want to surface the error cleanly should use ForInputE instead.
func ForInput(md protoreflect.MessageDescriptor, opts Options) map[string]any {
	s, err := ForInputE(md, opts)
	if err != nil {
		panic(err)
	}
	return s
}

// ForOutput returns the JSON Schema map for use as an MCP tool's OutputSchema.
// All fields are included.
//
// Panics if a field carries an invalid `// @example <json>` marker; callers
// that want to surface the error cleanly should use ForOutputE instead.
func ForOutput(md protoreflect.MessageDescriptor, opts Options) map[string]any {
	s, err := ForOutputE(md, opts)
	if err != nil {
		panic(err)
	}
	return s
}

// ForInputE is like ForInput but returns any comment-marker parse errors
// (e.g., invalid JSON in an `// @example` line) instead of panicking.
func ForInputE(md protoreflect.MessageDescriptor, opts Options) (_ map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ce, ok := r.(*commentError); ok {
				err = ce
				return
			}
			panic(r)
		}
	}()
	return messageSchema(md, opts, nil, isInputField), nil
}

// ForOutputE is like ForOutput but returns any comment-marker parse errors
// (e.g., invalid JSON in an `// @example` line) instead of panicking.
func ForOutputE(md protoreflect.MessageDescriptor, opts Options) (_ map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ce, ok := r.(*commentError); ok {
				err = ce
				return
			}
			panic(r)
		}
	}()
	return messageSchema(md, opts, nil, includeAllFields), nil
}

// commentError reports a structured-marker (e.g. @example) parse error
// inside a proto leading comment, with file:line info for diagnostics.
type commentError struct {
	File  string // proto file path as reported by the descriptor
	Line  int    // 1-based line number of the offending comment line, 0 if unknown
	Field string // fully-qualified field name (e.g., "pkg.Msg.field")
	Inner error
}

func (e *commentError) Error() string {
	loc := e.File
	if e.Line > 0 {
		loc = fmt.Sprintf("%s:%d", e.File, e.Line)
	}
	if loc == "" {
		loc = "<unknown>"
	}
	return fmt.Sprintf("%s: field %s: %v", loc, e.Field, e.Inner)
}

func (e *commentError) Unwrap() error { return e.Inner }

func includeAllFields(_ protoreflect.FieldDescriptor) bool { return true }

// isInputField drops OUTPUT_ONLY fields (AIP-203) from input schemas.
func isInputField(fd protoreflect.FieldDescriptor) bool {
	if !proto.HasExtension(fd.Options(), annotations.E_FieldBehavior) {
		return true
	}
	behaviors, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	return !slices.Contains(behaviors, annotations.FieldBehavior_OUTPUT_ONLY)
}

// messageSchema is the recursive walker; seen tracks per-FullName
// expansion depth so cycles break deterministically.
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
		// Use the protojson JSON name so the schema matches what
		// protojson.Marshal produces on the response side.
		name := fd.JSONName()

		// Real oneofs → anyOf {oneOf:...}; synthetic oneofs
		// (proto3 `optional`) fall through as ordinary optional fields.
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

	// Declaration-order iteration for deterministic output.
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

// fieldSchema builds the schema for one field, including list/map
// wrapping and buf.validate overlay.
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

	// Array wrapper and inner item both receive field-level
	// decorations so description renders regardless of which shape
	// the caller inspects. Repeated-specific rules stay on the
	// wrapper.
	if fd.IsList() {
		list := map[string]any{"type": "array", "items": schema}
		applyRepeatedRules(fd, list)
		decorateFieldSchema(fd, list)
		return list
	}
	decorateFieldSchema(fd, schema)
	return schema
}

// decorateFieldSchema applies field-level JSON Schema decorations:
// description from proto comments, examples from `// @example <json>`
// markers, and deprecated from [deprecated = true]. Existing
// description/examples are never overwritten.
//
// Invalid @example JSON panics with *commentError; ForInputE /
// ForOutputE surface it as a codegen error.
func decorateFieldSchema(fd protoreflect.FieldDescriptor, s map[string]any) {
	desc, examples := fieldCommentMeta(fd)
	if _, has := s["description"]; !has && desc != "" {
		s["description"] = desc
	}
	// Precedence for enum description: field-comment > enum-type-comment.
	if _, has := s["description"]; !has && fd.Kind() == protoreflect.EnumKind {
		if d := enumTypeDescription(fd.Enum()); d != "" {
			s["description"] = d
		}
	}
	if _, has := s["examples"]; !has && len(examples) > 0 {
		s["examples"] = examples
	}
	if fieldIsDeprecated(fd) {
		s["deprecated"] = true
	}
}

// fieldCommentMeta returns cleaned description and parsed examples
// from fd's leading comment (falling back to trailing).
func fieldCommentMeta(fd protoreflect.FieldDescriptor) (string, []any) {
	loc := fd.ParentFile().SourceLocations().ByDescriptor(fd)
	desc, examples := parseComment(loc.LeadingComments, fd, loc.StartLine+1)
	if desc == "" && len(examples) == 0 {
		desc, examples = parseComment(loc.TrailingComments, fd, loc.StartLine+1)
	}
	return desc, examples
}

// parseComment splits raw into description lines and examples
// (@example payloads). baseLine is the 1-based line of raw's first
// line (0 when unknown). Invalid @example JSON panics with
// *commentError.
func parseComment(raw string, fd protoreflect.FieldDescriptor, baseLine int) (string, []any) {
	if raw == "" {
		return "", nil
	}
	var descLines []string
	var examples []any
	prefixes := []string{"buf:lint:", "@ignore-comment", "@exclude"}
	lines := strings.Split(raw, "\n")
skip:
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				continue skip
			}
		}
		if rest, ok := strings.CutPrefix(trimmed, "@example "); ok {
			rest = strings.TrimSpace(rest)
			var v any
			if err := json.Unmarshal([]byte(rest), &v); err != nil {
				srcLine := 0
				if baseLine > 0 {
					srcLine = baseLine + i
				}
				panic(&commentError{
					File:  fd.ParentFile().Path(),
					Line:  srcLine,
					Field: string(fd.FullName()),
					Inner: fmt.Errorf("invalid JSON in @example: %w", err),
				})
			}
			examples = append(examples, v)
			continue
		}
		// The no-payload @example form is preserved as description.
		descLines = append(descLines, trimmed)
	}
	return strings.TrimSpace(strings.Join(descLines, "\n")), examples
}

// fieldIsDeprecated reports whether fd carries [deprecated = true].
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

// applyRepeatedRules copies buf.validate min_items / max_items /
// unique onto the array schema. Element-level rules are handled
// separately via GetRepeated().GetItems().
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

// mapFieldSchema renders a proto map as a JSON object with
// propertyNames constraining the key format.
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

// keyConstraints returns a JSON Schema for the stringified map key.
// Protojson keys are always strings; pattern/enum checks guard
// numeric/bool key types against bogus input.
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

// messageFieldSchema handles nested messages and well-known types
// that protojson renders as primitives.
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
		// protojson serializes Any as an open object with @type plus
		// the packed message's fields (or a single "value" for WKT
		// payloads); the inner type is unknown at codegen time.
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

// enumFieldSchema emits string enum values using proto value names,
// narrowed by buf.validate when applicable. enumDescriptions is
// emitted in parallel order when any value has a leading comment.
func enumFieldSchema(fd protoreflect.FieldDescriptor) map[string]any {
	values := fd.Enum().Values()

	// Collect in emission order so enum and enumDescriptions align.
	var selected []protoreflect.EnumValueDescriptor

	rules := fieldRules(fd)
	if rules != nil && rules.GetEnum() != nil {
		er := rules.GetEnum()
		switch {
		case er.HasConst():
			if ev := values.ByNumber(protoreflect.EnumNumber(er.GetConst())); ev != nil {
				selected = []protoreflect.EnumValueDescriptor{ev}
			}
		case len(er.GetIn()) > 0:
			selected = make([]protoreflect.EnumValueDescriptor, 0, len(er.GetIn()))
			for _, n := range er.GetIn() {
				if ev := values.ByNumber(protoreflect.EnumNumber(n)); ev != nil {
					selected = append(selected, ev)
				}
			}
		case er.GetDefinedOnly() || len(er.GetNotIn()) > 0:
			excluded := make(map[int32]struct{}, len(er.GetNotIn()))
			for _, n := range er.GetNotIn() {
				excluded[n] = struct{}{}
			}
			selected = make([]protoreflect.EnumValueDescriptor, 0, values.Len())
			for i := range values.Len() {
				v := values.Get(i)
				if _, skip := excluded[int32(v.Number())]; skip {
					continue
				}
				selected = append(selected, v)
			}
		}
	}
	if selected == nil {
		selected = make([]protoreflect.EnumValueDescriptor, 0, values.Len())
		for i := range values.Len() {
			selected = append(selected, values.Get(i))
		}
	}

	names := make([]string, len(selected))
	descriptions := make([]string, len(selected))
	anyDesc := false
	for i, ev := range selected {
		names[i] = string(ev.Name())
		d := enumValueDescription(ev)
		descriptions[i] = d
		if d != "" {
			anyDesc = true
		}
	}

	out := map[string]any{"type": "string", "enum": names}
	if anyDesc {
		out["enumDescriptions"] = descriptions
	}
	return out
}

// enumValueDescription returns the cleaned leading comment for ev.
func enumValueDescription(ev protoreflect.EnumValueDescriptor) string {
	loc := ev.ParentFile().SourceLocations().ByDescriptor(ev)
	return strings.TrimSpace(CleanComment(loc.LeadingComments))
}

// enumTypeDescription returns the cleaned leading comment on the
// enum type itself; used as a fallback for fields without their own.
func enumTypeDescription(ed protoreflect.EnumDescriptor) string {
	loc := ed.ParentFile().SourceLocations().ByDescriptor(ed)
	return strings.TrimSpace(CleanComment(loc.LeadingComments))
}

func scalarFieldSchema(fd protoreflect.FieldDescriptor) map[string]any {
	s := map[string]any{"type": kindToJSONType(fd.Kind())}
	if fd.Kind() == protoreflect.BytesKind {
		s["contentEncoding"] = "base64"
		s["format"] = "byte"
	}
	return s
}

// kindToJSONType maps a proto Kind to a JSON Schema type. 64-bit
// ints render as strings to avoid JavaScript precision loss.
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

// isRequired reads google.api.field_behavior=REQUIRED (AIP-203) and
// buf.validate.field.required.
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

// fieldRules returns the buf.validate FieldRules on fd, or nil.
func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	if !proto.HasExtension(fd.Options(), validate.E_Field) {
		return nil
	}
	rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
	return rules
}

// extractValidateConstraints translates buf.validate rules into JSON
// Schema keywords. For repeated fields, per-item constraints live
// under GetRepeated().GetItems().
func extractValidateConstraints(fd protoreflect.FieldDescriptor) map[string]any {
	out := map[string]any{}
	rules := fieldRules(fd)
	if rules == nil {
		return out
	}

	if fd.IsList() {
		if items := rules.GetRepeated().GetItems(); items != nil {
			rules = items
		}
	}

	overlay(out, convertFieldRules(rules))
	return out
}

// convertFieldRules maps buf.validate FieldRules to JSON Schema.
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

// applyStringRules maps buf.validate.StringRules to JSON Schema.
// Non-standard format names (uri-reference, ipv4-with-prefixlen, etc.)
// are accepted per JSON Schema draft 2020-12.
func applyStringRules(s *validate.StringRules, out map[string]any) {
	// buf.validate forbids setting multiple formats (oneof).
	switch {
	case s.GetUuid():
		out["format"] = "uuid"
	case s.GetTuuid():
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
		// JSON Schema has no "not-enum"; express as not/enum.
		out["not"] = map[string]any{"enum": append([]string(nil), s.GetNotIn()...)}
	}
}

// applyBytesRules maps bytes rules. min/maxLength here bound the
// base64 string length, not the raw byte count; protovalidate on the
// gRPC side is authoritative.
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

// The Int32/UInt32/Int64/UInt64/Float/Double helpers are structurally
// identical but typed against distinct protovalidate rule messages;
// generic-izing them would obscure the code.
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

// Int64 fields render as JSON strings per protojson (precision
// beyond 2^53), so numeric keywords emitted here are LLM hints, not
// enforced constraints; protovalidate is authoritative. Same for
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

// CleanComment strips tool pragmas (buf:lint:, @ignore-comment,
// @exclude) and normalizes whitespace for use as a description.
func CleanComment(comment string) string {
	var out []string
	prefixes := []string{"buf:lint:", "@ignore-comment", "@exclude"}
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
