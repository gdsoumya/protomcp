package protomcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResultProcessor inspects and/or mutates a CallToolResult just before
// it is returned to the MCP client. Typical uses:
//
//   - redact sensitive fields (emails, tokens, SSNs) from the response
//   - enrich the response with client-specific metadata
//   - rewrite the response shape for downstream consumers
//
// Processors run in registration order. The input `result` is never nil
// when a processor is called; processors that want to drop the response
// entirely should return a minimal replacement (e.g. an empty
// CallToolResult). Returning a nil result is treated as a passthrough.
//
// Processors see every CallToolResult that is about to reach the
// client, including IsError results synthesized by the ErrorHandler, so
// a single redaction processor covers both success and failure paths.
type ResultProcessor func(ctx context.Context, req *mcp.CallToolRequest, result *mcp.CallToolResult) (*mcp.CallToolResult, error)

// WithResultProcessor appends one or more ResultProcessors to the
// Server. They run after the middleware chain completes (and after
// ErrorHandler translation, when applicable) in registration order.
//
// Panics on a nil entry so the problem surfaces at construction time
// rather than on every subsequent tool call.
func WithResultProcessor(p ...ResultProcessor) ServerOption {
	return func(s *Server) {
		for i, proc := range p {
			if proc == nil {
				panic(fmt.Sprintf("protomcp: WithResultProcessor received nil at index %d", i))
			}
		}
		s.processors = append(s.processors, p...)
	}
}
