package protomcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ProgressTokenHeader is the default gRPC metadata key generated tool
// handlers use to forward the MCP client's progress token upstream.
const ProgressTokenHeader = "mcp-progress-token"

// Server wraps an mcp.Server with composable middleware chains, error
// handlers, and result processors per MCP primitive. Implements
// http.Handler.
type Server struct {
	sdk       *mcp.Server
	httpInner http.Handler

	toolPipeline         pipeline[*mcp.CallToolRequest, *mcp.CallToolResult]
	promptPipeline       pipeline[*mcp.GetPromptRequest, *mcp.GetPromptResult]
	resourceReadPipeline pipeline[*mcp.ReadResourceRequest, *mcp.ReadResourceResult]
	resourceListPipeline pipeline[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]

	// Only one lister is allowed; RegisterResourceLister panics on the
	// second call.
	listerRegMu sync.Mutex
	lister      ResourceLister

	progressTokenHeader string

	promptCompletionsMu sync.RWMutex
	promptCompletions   map[promptArgKey][]string

	sdkOpts  *mcp.ServerOptions
	httpOpts *mcp.StreamableHTTPOptions

	protoMarshal   protojson.MarshalOptions
	protoUnmarshal protojson.UnmarshalOptions
}

// ServerOption configures a Server at construction time. Variadic
// collectors panic on nil elements; single-value replacements treat nil
// or empty input as a no-op.
type ServerOption func(*Server)

// WithToolMiddleware appends ToolMiddleware to the chain. The first
// argument is the outermost wrapper; multiple calls accumulate. Panics
// on nil entries.
func WithToolMiddleware(m ...ToolMiddleware) ServerOption {
	return func(s *Server) {
		for i, mw := range m {
			if mw == nil {
				panic(fmt.Sprintf("protomcp: WithToolMiddleware received nil at index %d", i))
			}
		}
		s.toolPipeline.middleware = append(s.toolPipeline.middleware, m...)
	}
}

// WithToolErrorHandler replaces the default error handler. See
// DefaultToolErrorHandler for the fallback mapping.
func WithToolErrorHandler(h ToolErrorHandler) ServerOption {
	return func(s *Server) {
		if h != nil {
			s.toolPipeline.errHandler = h
		}
	}
}

// WithSDKOptions forwards *mcp.ServerOptions to mcp.NewServer. A
// caller-supplied CompletionHandler is wrapped so a non-empty caller
// result wins and generator prompt-argument completions act as a
// fallback; all other fields pass through untouched.
func WithSDKOptions(o *mcp.ServerOptions) ServerOption {
	return func(s *Server) { s.sdkOpts = o }
}

// WithProgressTokenHeader overrides the gRPC metadata key used to
// forward the MCP progressToken. Empty is ignored.
func WithProgressTokenHeader(name string) ServerOption {
	return func(s *Server) {
		if name != "" {
			s.progressTokenHeader = name
		}
	}
}

// WithProtoJSONMarshal overrides the protojson.MarshalOptions used by
// generated handlers for outbound MCP JSON and Mustache template
// contexts. Default is EmitDefaultValues=true so output matches the
// declared schema and templates over zero values render "0" rather than
// an empty string. Does not affect DefaultToolErrorHandler's
// google.rpc.Status marshaling.
func WithProtoJSONMarshal(o protojson.MarshalOptions) ServerOption {
	return func(s *Server) { s.protoMarshal = o }
}

// WithProtoJSONUnmarshal overrides the protojson.UnmarshalOptions used
// to parse MCP arguments into the upstream gRPC request. Default is
// strict parsing (DiscardUnknown=false) so unexpected fields surface as
// an error rather than silent data loss.
func WithProtoJSONUnmarshal(o protojson.UnmarshalOptions) ServerOption {
	return func(s *Server) { s.protoUnmarshal = o }
}

// WithHTTPOptions forwards *mcp.StreamableHTTPOptions to
// mcp.NewStreamableHTTPHandler. No effect on ServeStdio.
func WithHTTPOptions(o *mcp.StreamableHTTPOptions) ServerOption {
	return func(s *Server) { s.httpOpts = o }
}

// New constructs a Server. name and version populate the MCP
// Implementation block sent during handshake.
func New(name, version string, opts ...ServerOption) *Server {
	s := &Server{
		promptCompletions: map[promptArgKey][]string{},
		protoMarshal:      protojson.MarshalOptions{EmitDefaultValues: true},
	}
	s.toolPipeline.errHandler = DefaultToolErrorHandler
	s.promptPipeline.errHandler = DefaultPromptErrorHandler
	s.resourceReadPipeline.errHandler = DefaultResourceReadErrorHandler
	s.resourceListPipeline.errHandler = DefaultResourceListErrorHandler
	for _, o := range opts {
		o(s)
	}
	if s.sdkOpts == nil {
		s.sdkOpts = &mcp.ServerOptions{}
	}
	s.sdkOpts = s.installCompletionHandler(s.sdkOpts)
	s.sdk = mcp.NewServer(&mcp.Implementation{Name: name, Version: version}, s.sdkOpts)
	s.httpInner = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.sdk
	}, s.httpOpts)
	return s
}

// ProgressTokenHeader returns the configured gRPC metadata key for
// forwarding the MCP progressToken, or the default when unset.
func (s *Server) ProgressTokenHeader() string {
	if s.progressTokenHeader == "" {
		return ProgressTokenHeader
	}
	return s.progressTokenHeader
}

// SDK returns the underlying mcp.Server. Primitives registered directly
// on it bypass every protomcp middleware, processor, and error handler;
// CompletionHandler set via WithSDKOptions is the only exception and is
// wrapped rather than replaced.
func (s *Server) SDK() *mcp.Server {
	return s.sdk
}

// ToolChain composes the configured ToolMiddleware around final. The
// first registered middleware is the outermost wrapper.
func (s *Server) ToolChain(final ToolHandler) ToolHandler {
	return s.toolPipeline.chain(final)
}

// HandleToolError runs the configured ToolErrorHandler. Falls back to
// the original error if the handler returns (nil, nil).
func (s *Server) HandleToolError(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
	if err == nil {
		return nil, nil
	}
	p := &s.toolPipeline
	h := p.errHandler
	if h == nil {
		h = DefaultToolErrorHandler
	}
	result, herr := h(ctx, req, err)
	switch {
	case result != nil && herr != nil:
		return nil, herr
	case result != nil:
		return result, nil
	case herr != nil:
		return nil, herr
	default:
		return nil, err
	}
}

// FinishToolCall is the wrap-up entry point generated code uses. The
// second return is the tool's Out value: result.StructuredContent for
// success, untyped nil for IsError results so the SDK skips validating
// the google.rpc.Status shape against the success OutputSchema.
// ToolResultProcessor errors propagate as JSON-RPC errors without
// re-entering ToolErrorHandler.
func (s *Server) FinishToolCall(ctx context.Context, req *mcp.CallToolRequest, g *GRPCData, result *mcp.CallToolResult, err error) (*mcp.CallToolResult, any, error) {
	result, fErr := s.toolPipeline.finish(ctx, req, g, result, err)
	if fErr != nil {
		return nil, nil, fErr
	}
	if result == nil {
		panic(fmt.Sprintf(
			"protomcp: FinishToolCall reached with no result and no error; "+
				"a ToolMiddleware returned (nil, nil) (tool=%q)",
			toolNameFromRequest(req),
		))
	}
	if result.IsError {
		return result, nil, nil
	}
	return result, result.StructuredContent, nil
}

// toolNameFromRequest returns the tool name or "<unknown>".
func toolNameFromRequest(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil || req.Params.Name == "" {
		return "<unknown>"
	}
	return req.Params.Name
}

// ServeHTTP makes Server an http.Handler. HTTP-layer concerns (auth,
// CORS, rate limiting, access logs) belong in http.Handler middleware
// wrapped around the Server; per-call concerns that write outgoing gRPC
// metadata go in a ToolMiddleware.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpInner.ServeHTTP(w, r)
}

// MarshalProto serializes m using the configured protojson.MarshalOptions.
func (s *Server) MarshalProto(m proto.Message) ([]byte, error) {
	return s.protoMarshal.Marshal(m)
}

// UnmarshalProto parses data into m using the configured protojson.UnmarshalOptions.
func (s *Server) UnmarshalProto(data []byte, m proto.Message) error {
	return s.protoUnmarshal.Unmarshal(data, m)
}

// ServeStdio runs the server over the SDK's stdio transport. HTTP
// middleware has no effect; stdio transports rely on the parent process
// for auth per MCP spec guidance.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.sdk.Run(ctx, &mcp.StdioTransport{})
}
