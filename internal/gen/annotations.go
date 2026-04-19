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

// toolOptionsFor returns the protomcp.v1.ToolOptions attached to m, if any.
// Presence of the option is what opts an RPC in to MCP tool generation —
// unannotated methods are never exposed and this function returns (nil, false)
// for them.
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

// serviceOptionsFor returns the protomcp.v1.ServiceOptions attached to s,
// or nil if the service carries no such option. Absence is treated as the
// zero value (no prefix).
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

// hasAnyAnnotatedMethod reports whether f contains at least one RPC that
// carries the protomcp.v1.tool option. Files without any annotated methods
// are skipped entirely so we don't emit empty *.mcp.pb.go files.
func hasAnyAnnotatedMethod(f *protogen.File) bool {
	for _, svc := range f.Services {
		for _, m := range svc.Methods {
			if _, ok := toolOptionsFor(m); ok {
				return true
			}
		}
	}
	return false
}
