package protomcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// ErrorHandler decides how a Go error raised by middleware or the tool
// handler is surfaced to the MCP client. Exactly one of the returned
// values must be non-nil: a non-nil *mcp.CallToolResult is delivered as
// a successful JSON-RPC response (typically with IsError set); a
// non-nil error is propagated to the SDK and becomes a JSON-RPC error.
//
// To ensure an error is delivered to the client as a JSON-RPC protocol
// error (rather than being wrapped by the SDK into a CallToolResult with
// IsError=true), return a *jsonrpc.Error. Any other error type is wrapped
// by the SDK into a tool-result with IsError=true.
type ErrorHandler func(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error)

// grpcCodeToJSONRPCCode maps a gRPC code to a JSON-RPC error code used by
// DefaultErrorHandler. Values are drawn from the standard JSON-RPC error
// range (-32000..-32099, server-defined) because the spec reserves
// -32700..-32600 for protocol-level failures.
func grpcCodeToJSONRPCCode(c codes.Code) int64 {
	switch c {
	case codes.Unauthenticated:
		return -32001
	case codes.PermissionDenied:
		return -32002
	case codes.Canceled:
		return -32003
	case codes.DeadlineExceeded:
		return -32004
	default:
		return -32000
	}
}

// DefaultErrorHandler is the ErrorHandler applied when none is
// configured via WithErrorHandler. Mapping:
//
//   - gRPC Unauthenticated / PermissionDenied / Canceled / DeadlineExceeded
//     -> JSON-RPC error wrapped as a *jsonrpc.Error. The SDK routes
//     *jsonrpc.Error as a real JSON-RPC protocol error; any other error
//     type would be wrapped by the SDK into a CallToolResult with
//     IsError=true, which is NOT what we want for these codes.
//   - Any other gRPC status -> CallToolResult{IsError: true} whose text
//     content is "Code: Message" and whose structured content is the
//     protojson-serialized google.rpc.Status (preserves details).
//   - Any non-gRPC error -> CallToolResult{IsError: true} whose text
//     content is err.Error().
func DefaultErrorHandler(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
	if err == nil {
		return nil, nil
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unauthenticated, codes.PermissionDenied, codes.Canceled, codes.DeadlineExceeded:
			return nil, &jsonrpc.Error{
				Code:    grpcCodeToJSONRPCCode(st.Code()),
				Message: fmt.Sprintf("%s: %s", st.Code(), st.Message()),
			}
		}
		structured, mErr := protojson.Marshal(st.Proto())
		result := &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %s", st.Code(), st.Message())}},
		}
		if mErr == nil {
			result.StructuredContent = json.RawMessage(structured)
		}
		return result, nil
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}, nil
}
