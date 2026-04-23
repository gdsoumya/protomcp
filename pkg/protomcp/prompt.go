package protomcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PromptHandler is the inner step in the prompt middleware chain.
//
// Alias for Handler[*mcp.GetPromptRequest, *mcp.GetPromptResult].
type PromptHandler = Handler[*mcp.GetPromptRequest, *mcp.GetPromptResult]

// PromptMiddleware wraps a PromptHandler.
//
// Alias for Middleware[*mcp.GetPromptRequest, *mcp.GetPromptResult].
type PromptMiddleware = Middleware[*mcp.GetPromptRequest, *mcp.GetPromptResult]

// PromptResultProcessor inspects or mutates a GetPromptResult before it
// reaches the client.
//
// Alias for ResultProcessor[*mcp.GetPromptRequest, *mcp.GetPromptResult].
type PromptResultProcessor = ResultProcessor[*mcp.GetPromptRequest, *mcp.GetPromptResult]

// PromptErrorHandler decides how a Go error reaches the client. See
// DefaultPromptErrorHandler.
//
// Alias for ErrorHandler[*mcp.GetPromptRequest, *mcp.GetPromptResult].
type PromptErrorHandler = ErrorHandler[*mcp.GetPromptRequest, *mcp.GetPromptResult]

// WithPromptMiddleware appends PromptMiddleware to the chain. First
// argument is outermost; multiple calls accumulate. Nil entries panic.
func WithPromptMiddleware(m ...PromptMiddleware) ServerOption {
	return func(s *Server) {
		for i, mw := range m {
			if mw == nil {
				panic(fmt.Sprintf("protomcp: WithPromptMiddleware received nil at index %d", i))
			}
		}
		s.promptPipeline.middleware = append(s.promptPipeline.middleware, m...)
	}
}

// WithPromptResultProcessor appends PromptResultProcessors. They run
// in registration order after the middleware chain and error handler.
// Nil entries panic.
func WithPromptResultProcessor(p ...PromptResultProcessor) ServerOption {
	return func(s *Server) {
		for i, proc := range p {
			if proc == nil {
				panic(fmt.Sprintf("protomcp: WithPromptResultProcessor received nil at index %d", i))
			}
		}
		s.promptPipeline.processors = append(s.promptPipeline.processors, p...)
	}
}

// WithPromptErrorHandler replaces the default prompt error handler.
// Nil is ignored.
func WithPromptErrorHandler(h PromptErrorHandler) ServerOption {
	return func(s *Server) {
		if h != nil {
			s.promptPipeline.errHandler = h
		}
	}
}

// DefaultPromptErrorHandler maps any error to a JSON-RPC protocol
// error via grpcErrorToJSONRPC; prompts have no IsError result shape.
func DefaultPromptErrorHandler(_ context.Context, _ *mcp.GetPromptRequest, err error) (*mcp.GetPromptResult, error) {
	return nil, grpcErrorToJSONRPC(err)
}

// PromptChain composes the configured middleware around final. The
// first registered middleware is the outermost wrapper.
func (s *Server) PromptChain(final PromptHandler) PromptHandler {
	return s.promptPipeline.chain(final)
}

// HandlePromptError runs the configured PromptErrorHandler. Falls back
// to the original error if the handler returns (nil, nil).
func (s *Server) HandlePromptError(ctx context.Context, req *mcp.GetPromptRequest, err error) (*mcp.GetPromptResult, error) {
	if err == nil {
		return nil, nil
	}
	p := &s.promptPipeline
	h := p.errHandler
	if h == nil {
		h = DefaultPromptErrorHandler
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

// FinishPromptGet is the wrap-up entry point for prompts. Panics if a
// user middleware returned (nil, nil).
func (s *Server) FinishPromptGet(ctx context.Context, req *mcp.GetPromptRequest, g *GRPCData, result *mcp.GetPromptResult, err error) (*mcp.GetPromptResult, error) {
	result, fErr := s.promptPipeline.finish(ctx, req, g, result, err)
	if fErr != nil {
		return nil, fErr
	}
	if result == nil {
		panic(fmt.Sprintf(
			"protomcp: FinishPromptGet reached with no result and no error; "+
				"a PromptMiddleware returned (nil, nil) (prompt=%q)",
			promptNameFromRequest(req),
		))
	}
	return result, nil
}

// promptNameFromRequest returns the prompt name or "<unknown>".
func promptNameFromRequest(req *mcp.GetPromptRequest) string {
	if req == nil || req.Params == nil || req.Params.Name == "" {
		return "<unknown>"
	}
	return req.Params.Name
}
