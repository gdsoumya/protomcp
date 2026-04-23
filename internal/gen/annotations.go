// Package gen implements the code-generation internals of protoc-gen-mcp.
// It reads the protomcp.v1 method/service options, computes tool names and
// JSON Schemas for annotated RPCs, and emits one <file>.mcp.pb.go per input
// proto file containing the MCP tool registrations.
package gen

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// toolOptionsFor returns the protomcp.v1.ToolOptions attached to m.
// Presence of the option opts an RPC in to MCP tool generation.
func toolOptionsFor(m *protogen.Method) (*protomcpv1.ToolOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_Tool) {
		return nil, false
	}
	to, _ := proto.GetExtension(opts, protomcpv1.E_Tool).(*protomcpv1.ToolOptions)
	if to == nil {
		return nil, false
	}
	return to, true
}

// resourceListChangedOptionsFor returns the
// protomcp.v1.ResourceListChangedOptions attached to m.
//
//nolint:unparam // options shape matches sibling accessors for future-proofing
func resourceListChangedOptionsFor(m *protogen.Method) (*protomcpv1.ResourceListChangedOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_ResourceListChanged) {
		return nil, false
	}
	rlco, _ := proto.GetExtension(opts, protomcpv1.E_ResourceListChanged).(*protomcpv1.ResourceListChangedOptions)
	if rlco == nil {
		return nil, false
	}
	return rlco, true
}

// resourceTemplateOptionsFor returns the
// protomcp.v1.ResourceTemplateOptions attached to m.
func resourceTemplateOptionsFor(m *protogen.Method) (*protomcpv1.ResourceTemplateOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_ResourceTemplate) {
		return nil, false
	}
	ro, _ := proto.GetExtension(opts, protomcpv1.E_ResourceTemplate).(*protomcpv1.ResourceTemplateOptions)
	if ro == nil {
		return nil, false
	}
	return ro, true
}

// resourceListOptionsFor returns the protomcp.v1.ResourceListOptions
// attached to m.
func resourceListOptionsFor(m *protogen.Method) (*protomcpv1.ResourceListOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_ResourceList) {
		return nil, false
	}
	rlo, _ := proto.GetExtension(opts, protomcpv1.E_ResourceList).(*protomcpv1.ResourceListOptions)
	if rlo == nil {
		return nil, false
	}
	return rlo, true
}

// promptOptionsFor returns the protomcp.v1.PromptOptions attached to m.
func promptOptionsFor(m *protogen.Method) (*protomcpv1.PromptOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_Prompt) {
		return nil, false
	}
	po, _ := proto.GetExtension(opts, protomcpv1.E_Prompt).(*protomcpv1.PromptOptions)
	if po == nil {
		return nil, false
	}
	return po, true
}

// elicitationOptionsFor returns the protomcp.v1.ElicitationOptions
// attached to m. Elicitation modifies a tool annotation; the
// cross-check lives in generator.go.
func elicitationOptionsFor(m *protogen.Method) (*protomcpv1.ElicitationOptions, bool) {
	opts, ok := m.Desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return nil, false
	}
	if !proto.HasExtension(opts, protomcpv1.E_Elicitation) {
		return nil, false
	}
	eo, _ := proto.GetExtension(opts, protomcpv1.E_Elicitation).(*protomcpv1.ElicitationOptions)
	if eo == nil {
		return nil, false
	}
	return eo, true
}

// serviceOptionsFor returns the protomcp.v1.ServiceOptions attached to
// s, or nil. Absence is treated as the zero value (no prefix).
func serviceOptionsFor(s *protogen.Service) *protomcpv1.ServiceOptions {
	opts, ok := s.Desc.Options().(*descriptorpb.ServiceOptions)
	if !ok || opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, protomcpv1.E_Service) {
		return nil
	}
	so, _ := proto.GetExtension(opts, protomcpv1.E_Service).(*protomcpv1.ServiceOptions)
	return so
}

// hasAnyPrimitiveAnnotation reports whether m carries any protomcp.v1
// primitive annotation (tool, resource, resource_list,
// resource_list_changed, prompt).
func hasAnyPrimitiveAnnotation(m *protogen.Method) bool {
	if _, ok := toolOptionsFor(m); ok {
		return true
	}
	if _, ok := resourceTemplateOptionsFor(m); ok {
		return true
	}
	if _, ok := resourceListOptionsFor(m); ok {
		return true
	}
	if _, ok := resourceListChangedOptionsFor(m); ok {
		return true
	}
	if _, ok := promptOptionsFor(m); ok {
		return true
	}
	return false
}

// hasAnyAnnotatedMethod reports whether f contains at least one RPC
// the generator needs to process. Elicitation is included so a
// standalone elicitation reaches the validation pass (hard error)
// rather than silently skipping the file.
func hasAnyAnnotatedMethod(f *protogen.File) bool {
	for _, svc := range f.Services {
		for _, m := range svc.Methods {
			if hasAnyPrimitiveAnnotation(m) {
				return true
			}
			if _, ok := elicitationOptionsFor(m); ok {
				return true
			}
		}
	}
	return false
}

// methodClassification captures which primitive surfaces an RPC opts
// into. Multiple primitives are legal (e.g., tool + resource).
type methodClassification struct {
	asTool                bool
	asResourceTemplate    bool
	asResourceList        bool
	asResourceListChanged bool
	asPrompt              bool
}

// classifyMethod inspects m's annotations and returns which surfaces
// it opts into.
func classifyMethod(m *protogen.Method) methodClassification {
	var c methodClassification
	if _, ok := toolOptionsFor(m); ok {
		c.asTool = true
	}
	if _, ok := resourceTemplateOptionsFor(m); ok {
		c.asResourceTemplate = true
	}
	if _, ok := resourceListOptionsFor(m); ok {
		c.asResourceList = true
	}
	if _, ok := resourceListChangedOptionsFor(m); ok {
		c.asResourceListChanged = true
	}
	if _, ok := promptOptionsFor(m); ok {
		c.asPrompt = true
	}
	return c
}

// bindingMap converts a []*PlaceholderBinding into a
// (placeholder → field) map.
func bindingMap(in []*protomcpv1.PlaceholderBinding) map[string]string {
	out := make(map[string]string, len(in))
	for _, b := range in {
		out[b.GetPlaceholder()] = b.GetField()
	}
	return out
}
