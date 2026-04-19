package protomcp

import (
	"slices"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ClearOutputOnly zeros every field reachable from m whose descriptor
// carries google.api.field_behavior = OUTPUT_ONLY (AIP-203). Generated
// tool handlers call it after protojson.Unmarshal so the LLM (or a
// crafted client) cannot populate server-computed fields that the
// advertised input schema already hides.
//
// The walk is recursive: it descends into non-cleared message fields,
// each element of repeated<message> fields, and each value of
// map<K, message> fields. For a field that is itself OUTPUT_ONLY,
// the whole value is cleared (including any nested content) — we do
// not recurse into an already-cleared branch. A nil or invalid message
// is a no-op.
//
// Protobuf instances produced by protojson.Unmarshal are trees, so
// recursion terminates naturally without a cycle guard. Self-
// referential message types (e.g. a tree node with a child of its own
// type) simply have a finite depth at runtime.
func ClearOutputOnly(m proto.Message) {
	if m == nil {
		return
	}
	r := m.ProtoReflect()
	if !r.IsValid() {
		return
	}
	clearOutputOnlyReflect(r)
}

// clearOutputOnlyReflect is the recursive worker. It mutates r in
// place and returns nothing; the caller should not observe anything
// beyond the zeroed fields.
func clearOutputOnlyReflect(r protoreflect.Message) {
	fields := r.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)

		if hasOutputOnly(fd) {
			r.Clear(fd)
			continue
		}

		// Only message-valued branches need recursion — scalar, enum,
		// and bytes fields can't contain OUTPUT_ONLY annotations below
		// them. For maps the field-level Kind is synthetic MessageKind
		// (the map entry); we check MapValue().Kind() separately below.
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			continue
		}
		if !r.Has(fd) {
			continue
		}

		switch {
		case fd.IsMap():
			// Only map values carry messages; keys are always scalar.
			if fd.MapValue().Kind() != protoreflect.MessageKind {
				continue
			}
			r.Get(fd).Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
				clearOutputOnlyReflect(v.Message())
				return true
			})
		case fd.IsList():
			list := r.Get(fd).List()
			for j := range list.Len() {
				clearOutputOnlyReflect(list.Get(j).Message())
			}
		default:
			clearOutputOnlyReflect(r.Get(fd).Message())
		}
	}
}

// hasOutputOnly reports whether fd's FieldBehavior list includes
// OUTPUT_ONLY. AIP-203 allows multiple behaviors on one field, so we
// check for membership rather than equality.
func hasOutputOnly(fd protoreflect.FieldDescriptor) bool {
	opts := fd.Options()
	if opts == nil {
		return false
	}
	if !proto.HasExtension(opts, annotations.E_FieldBehavior) {
		return false
	}
	behaviors, _ := proto.GetExtension(opts, annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	return slices.Contains(behaviors, annotations.FieldBehavior_OUTPUT_ONLY)
}
