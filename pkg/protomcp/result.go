package protomcp

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolResultProcessor inspects or mutates a CallToolResult before it
// reaches the client. Processors see IsError results synthesized by
// ToolErrorHandler, so a redaction processor covers both success and
// failure paths.
//
// Alias for ResultProcessor[*mcp.CallToolRequest, *mcp.CallToolResult].
type ToolResultProcessor = ResultProcessor[*mcp.CallToolRequest, *mcp.CallToolResult]

// WithToolResultProcessor appends ToolResultProcessors. They run in
// registration order after the middleware chain and error handler.
// Nil entries panic.
func WithToolResultProcessor(p ...ToolResultProcessor) ServerOption {
	return func(s *Server) {
		for i, proc := range p {
			if proc == nil {
				panic(fmt.Sprintf("protomcp: WithToolResultProcessor received nil at index %d", i))
			}
		}
		s.toolPipeline.processors = append(s.toolPipeline.processors, p...)
	}
}
