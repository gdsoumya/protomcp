package protomcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps an mcp.Server with a composable Middleware chain, a
// configurable ErrorHandler, and an ordered list of ResultProcessors.
// It is the central object generated code registers tools against, and
// it implements http.ServeHTTP so callers can plug it directly into any
// HTTP framework (stdlib, gin, chi, echo, fiber, etc.) the same way
// grpc-gateway's runtime.ServeMux does.
type Server struct {
	sdk        *mcp.Server
	httpInner  http.Handler // cached mcp.NewStreamableHTTPHandler output
	middleware []Middleware
	processors []ResultProcessor
	errHandler ErrorHandler

	// sdkOpts / httpOpts are captured from WithSDKOptions / WithHTTPOptions
	// and forwarded to the upstream SDK at New-time. They are nil by
	// default, in which case the SDK applies its own defaults.
	sdkOpts  *mcp.ServerOptions
	httpOpts *mcp.StreamableHTTPOptions
}

// ServerOption configures a Server at construction time. Options are
// applied in order and later options override earlier ones for
// single-value settings; WithMiddleware appends across calls.
type ServerOption func(*Server)

// WithMiddleware appends one or more Middleware to the chain. The
// outermost Middleware is the first one passed — it observes the
// request first (pre-next) and the response last (post-next).
// Multiple calls to WithMiddleware accumulate.
//
// Example: WithMiddleware(auth, metrics, ratelimit) produces the call
// order auth-pre → metrics-pre → ratelimit-pre → final handler →
// ratelimit-post → metrics-post → auth-post.
//
// Panics on a nil Middleware entry: the failure surfaces at New-time,
// not on every subsequent tool call.
func WithMiddleware(m ...Middleware) ServerOption {
	return func(s *Server) {
		for i, mw := range m {
			if mw == nil {
				panic(fmt.Sprintf("protomcp: WithMiddleware received nil at index %d", i))
			}
		}
		s.middleware = append(s.middleware, m...)
	}
}

// WithErrorHandler replaces the default error handler. See
// DefaultErrorHandler for the fallback mapping.
func WithErrorHandler(h ErrorHandler) ServerOption {
	return func(s *Server) {
		if h != nil {
			s.errHandler = h
		}
	}
}

// WithSDKOptions forwards a *mcp.ServerOptions straight through to the
// underlying mcp.NewServer call. Use it to set upstream knobs such as
// Logger, Instructions, KeepAlive, PageSize, InitializedHandler,
// ProgressNotificationHandler, Capabilities, and so on — see the SDK's
// mcp.ServerOptions documentation for the full list.
//
// The options are applied verbatim; protomcp does not merge or override
// any fields. If called more than once, the last call wins.
func WithSDKOptions(o *mcp.ServerOptions) ServerOption {
	return func(s *Server) { s.sdkOpts = o }
}

// WithHTTPOptions forwards a *mcp.StreamableHTTPOptions straight through
// to mcp.NewStreamableHTTPHandler, which backs Server.ServeHTTP. Use it
// to configure session behavior (Stateful, SessionIDGenerator,
// GetSessionID), JSON-response vs streaming, and related transport
// tunables — see the SDK's mcp.StreamableHTTPOptions documentation for
// the full list.
//
// Has no effect on ServeStdio, which does not use an HTTP transport.
// If called more than once, the last call wins.
func WithHTTPOptions(o *mcp.StreamableHTTPOptions) ServerOption {
	return func(s *Server) { s.httpOpts = o }
}

// New constructs a Server. The supplied name and version populate the
// MCP Implementation block sent during handshake. Options are applied
// before the underlying mcp.Server and streamable-HTTP handler are
// constructed, so WithSDKOptions / WithHTTPOptions (and anything else
// that configures upstream state) take effect as expected.
func New(name, version string, opts ...ServerOption) *Server {
	s := &Server{errHandler: DefaultErrorHandler}
	for _, o := range opts {
		o(s)
	}
	s.sdk = mcp.NewServer(&mcp.Implementation{Name: name, Version: version}, s.sdkOpts)
	s.httpInner = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.sdk
	}, s.httpOpts)
	return s
}

// SDK returns the underlying mcp.Server so generated code (and
// advanced users) can call mcp.AddTool, AddPrompt, AddResource, etc.
// against it directly.
//
// SECURITY: tool handlers registered directly via the returned
// *mcp.Server bypass the protomcp.Middleware chain, the ResultProcessor
// list, and the ErrorHandler seam. Auth propagation, redaction, and
// error translation will NOT run for them. Use this escape hatch only
// for tools that don't need those layers — or re-implement the
// equivalents inside the handler you pass to mcp.AddTool.
func (s *Server) SDK() *mcp.Server {
	return s.sdk
}

// Chain composes the configured middleware around the given final
// Handler. Middleware is applied in reverse so the first registered
// middleware is the outermost wrapper.
func (s *Server) Chain(final Handler) Handler {
	for i := len(s.middleware) - 1; i >= 0; i-- {
		final = s.middleware[i](final)
	}
	return final
}

// HandleError runs the configured ErrorHandler and returns whatever it
// produced. Exactly one return is non-nil on normal use; if a broken
// handler returns (nil, nil) we fall back to the original error so the
// caller is never silently left with a pair of nils.
//
// Exposed so advanced users can drive the configured ErrorHandler from
// their own glue code; the generator uses FinishCall, which wraps this
// plus the ResultProcessor chain.
func (s *Server) HandleError(ctx context.Context, req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, error) {
	if err == nil {
		return nil, nil
	}
	h := s.errHandler
	if h == nil {
		h = DefaultErrorHandler
	}
	result, herr := h(ctx, req, err)
	switch {
	case result != nil && herr != nil:
		// Contract violation: prefer the error so nothing is silently
		// lost. The result is dropped but the caller still observes a
		// failure.
		return nil, herr
	case result != nil:
		return result, nil
	case herr != nil:
		return nil, herr
	default:
		return nil, err
	}
}

// FinishCall is the single wrap-up entry point generated code uses. It
// routes any tool-handler error through the configured ErrorHandler,
// then runs every registered ResultProcessor on the resulting
// CallToolResult (including IsError results synthesized by the error
// handler), and finally returns the SDK's 3-tuple shape.
//
// The second return (typed as `any`) is the tool's Out value. The SDK
// marshals it to JSON and validates it against the tool's OutputSchema.
// We return:
//   - result.StructuredContent for successful results: the SDK validates
//     the real tool output against the schema the generator emitted, so
//     a schema mismatch is caught on the serving side at dev time.
//   - untyped nil for IsError results: the SDK's nil check sees truly
//     nil and skips validation, which is what we want — an error result
//     carries a google.rpc.Status shape (from DefaultErrorHandler) that
//     does NOT match the success output schema, and validating it would
//     surface a confusing "missing required field" error that masks the
//     real failure.
//
// Any error produced by a ResultProcessor itself is propagated as a
// JSON-RPC error (the processor chain does not re-enter the
// ErrorHandler, on the theory that a broken processor is a bug the
// caller wants to see, not quietly mask).
func (s *Server) FinishCall(ctx context.Context, req *mcp.CallToolRequest, result *mcp.CallToolResult, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		var perr error
		result, perr = s.HandleError(ctx, req, err)
		if perr != nil {
			return nil, nil, perr
		}
	}
	if result == nil {
		// A middleware returned (nil, nil) or the ErrorHandler produced
		// nothing. That's a programming error in the user's code — fail
		// loudly rather than hand the SDK an ambiguous empty response.
		panic(fmt.Sprintf(
			"protomcp: FinishCall reached with no result and no error; "+
				"a Middleware or ErrorHandler returned (nil, nil) (tool=%q)",
			toolNameFromRequest(req),
		))
	}
	for _, p := range s.processors {
		next, pErr := p(ctx, req, result)
		if pErr != nil {
			return nil, nil, pErr
		}
		if next != nil {
			result = next
		}
	}
	if result.IsError {
		return result, nil, nil
	}
	return result, result.StructuredContent, nil
}

// toolNameFromRequest extracts a best-effort tool name for panic messages;
// returns "<unknown>" when the request is nil or the field is empty.
func toolNameFromRequest(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil || req.Params.Name == "" {
		return "<unknown>"
	}
	return req.Params.Name
}

// ServeHTTP makes Server an http.Handler, following the grpc-gateway
// convention where the mux itself is the handler. Compose with any HTTP
// framework:
//
//	// stdlib
//	http.ListenAndServe(":8080", srv)
//
//	// with stdlib middleware
//	http.ListenAndServe(":8080", logging(auth(srv)))
//
//	// chi
//	r := chi.NewRouter(); r.Use(auth); r.Mount("/mcp", srv)
//
//	// gin
//	g := gin.New(); g.Any("/mcp/*any", gin.WrapH(srv))
//
// HTTP-layer concerns (authentication, CORS, rate limiting, access logs)
// belong in stdlib http.Handler middleware wrapped around the Server.
// Per-tool-call concerns that need to write outgoing gRPC metadata go in
// a protomcp.Middleware via WithMiddleware.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpInner.ServeHTTP(w, r)
}

// ServeStdio runs the server over the SDK's stdio transport. HTTP
// middleware has no effect here because there is no HTTP layer; per MCP
// spec guidance, stdio transports rely on the parent process for auth
// (env vars, file permissions, etc.) rather than request-level checks.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.sdk.Run(ctx, &mcp.StdioTransport{})
}
