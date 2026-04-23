package protomcp

import (
	"slices"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// clearOutputOnlyMaxDepth bounds recursion so self-referential protos
// (Struct/Value) cannot exhaust the stack on a malicious payload.
// Fields below the limit retain their caller-supplied value.
const clearOutputOnlyMaxDepth = 100

// ClearOutputOnly zeros every field reachable from m whose descriptor
// carries google.api.field_behavior = OUTPUT_ONLY (AIP-203). Recursion
// is capped at clearOutputOnlyMaxDepth.
func ClearOutputOnly(m proto.Message) {
	if m == nil {
		return
	}
	r := m.ProtoReflect()
	if !r.IsValid() {
		return
	}
	clearOutputOnlyReflect(r, 0)
}

// clearOutputOnlyReflect is the recursive worker; depth short-circuits
// at clearOutputOnlyMaxDepth.
func clearOutputOnlyReflect(r protoreflect.Message, depth int) {
	if depth >= clearOutputOnlyMaxDepth {
		return
	}
	fields := r.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)

		if hasOutputOnly(fd) {
			r.Clear(fd)
			continue
		}

		// Only message-valued branches need recursion. For maps the
		// field Kind is MessageKind (the map entry); MapValue().Kind()
		// is checked below.
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			continue
		}
		if !r.Has(fd) {
			continue
		}

		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() != protoreflect.MessageKind {
				continue
			}
			r.Get(fd).Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
				clearOutputOnlyReflect(v.Message(), depth+1)
				return true
			})
		case fd.IsList():
			list := r.Get(fd).List()
			for j := range list.Len() {
				clearOutputOnlyReflect(list.Get(j).Message(), depth+1)
			}
		default:
			clearOutputOnlyReflect(r.Get(fd).Message(), depth+1)
		}
	}
}

// hasOutputOnly reports whether fd's FieldBehavior list includes
// OUTPUT_ONLY. AIP-203 allows multiple behaviors per field.
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
