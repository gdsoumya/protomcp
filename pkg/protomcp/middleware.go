// Package protomcp is a thin runtime glue layer on top of
// github.com/modelcontextprotocol/go-sdk/mcp. Generated code from
// protoc-gen-mcp wires concrete gRPC clients into this runtime, which
// provides the middleware chain, context propagation, transport
// selection, default error mapping, and result post-processing.
package protomcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// GRPCData bundles the gRPC side of one MCP call. A single *GRPCData
// flows through the whole pipeline; Output is nil until the final
// handler runs.
type GRPCData struct {
	// Input is the typed proto request.
	Input proto.Message

	// Output is the typed proto response; nil before the final handler runs.
	Output proto.Message

	// Metadata is the outgoing gRPC metadata. Prefer SetMetadata for
	// values from untrusted sources.
	Metadata metadata.MD
}

// SetMetadata writes key=value into the outgoing gRPC metadata after
// running value through SanitizeMetadataValue, so CR/LF/NUL cannot
// reach the upstream hop. Any existing values for key are replaced.
func (g *GRPCData) SetMetadata(key, value string) {
	if g == nil {
		return
	}
	if g.Metadata == nil {
		g.Metadata = metadata.MD{}
	}
	g.Metadata.Set(key, SanitizeMetadataValue(value))
}

// MCPData bundles the MCP request and the current result for a
// ResultProcessor. Output assignments by a processor replace Output for
// subsequent processors.
type MCPData[Req, Res any] struct {
	Input  Req
	Output Res
}

// ToolHandler is the inner step in the tool-call middleware chain.
//
// Alias for Handler[*mcp.CallToolRequest, *mcp.CallToolResult].
type ToolHandler = Handler[*mcp.CallToolRequest, *mcp.CallToolResult]

// ToolMiddleware wraps a ToolHandler. HTTP-layer concerns belong in
// http.Handler middleware wrapped around the Server.
//
// Alias for Middleware[*mcp.CallToolRequest, *mcp.CallToolResult].
type ToolMiddleware = Middleware[*mcp.CallToolRequest, *mcp.CallToolResult]
