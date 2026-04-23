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

// ToolErrorHandler decides how a Go error is surfaced to the MCP
// client. Return a *jsonrpc.Error to surface a JSON-RPC protocol error;
// any other error type is wrapped by the SDK into a CallToolResult with
// IsError=true.
//
// Alias for ErrorHandler[*mcp.CallToolRequest, *mcp.CallToolResult].
type ToolErrorHandler = ErrorHandler[*mcp.CallToolRequest, *mcp.CallToolResult]

// grpcCodeToJSONRPCCode maps a gRPC code to a JSON-RPC error code in
// the server-defined range (-32000..-32099); the JSON-RPC spec reserves
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

// DefaultToolErrorHandler is the fallback ToolErrorHandler. Maps
// Unauthenticated / PermissionDenied / Canceled / DeadlineExceeded to
// JSON-RPC protocol errors; any other gRPC status becomes a
// CallToolResult{IsError: true} carrying the protojson-serialized
// google.rpc.Status as StructuredContent; non-gRPC errors become an
// IsError result with err.Error() as text.
func DefaultToolErrorHandler(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
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

// grpcErrorToJSONRPC wraps err in a *jsonrpc.Error for primitives
// without an IsError result shape (resources/read, resources/list,
// prompts/get). gRPC status errors preserve their code; others map to
// -32000.
func grpcErrorToJSONRPC(err error) error {
	if err == nil {
		return nil
	}
	// JSON-RPC -32602 Invalid params for client-supplied cursor failures.
	if IsInvalidCursorError(err) {
		return &jsonrpc.Error{Code: -32602, Message: err.Error()}
	}
	if st, ok := status.FromError(err); ok {
		return &jsonrpc.Error{
			Code:    grpcCodeToJSONRPCCode(st.Code()),
			Message: fmt.Sprintf("%s: %s", st.Code(), st.Message()),
		}
	}
	return &jsonrpc.Error{Code: -32000, Message: err.Error()}
}
