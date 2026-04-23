package protomcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceReadHandler is the inner step in the resource-read chain.
//
// Alias for Handler[*mcp.ReadResourceRequest, *mcp.ReadResourceResult].
type ResourceReadHandler = Handler[*mcp.ReadResourceRequest, *mcp.ReadResourceResult]

// ResourceListHandler is the inner step in the resource-list chain.
//
// Alias for Handler[*mcp.ListResourcesRequest, *mcp.ListResourcesResult].
type ResourceListHandler = Handler[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]

// ResourceReadMiddleware wraps a ResourceReadHandler.
//
// Alias for Middleware[*mcp.ReadResourceRequest, *mcp.ReadResourceResult].
type ResourceReadMiddleware = Middleware[*mcp.ReadResourceRequest, *mcp.ReadResourceResult]

// ResourceListMiddleware wraps a ResourceListHandler. Pagination
// helpers (OffsetPagination, PageTokenPagination) return one of these
// paired with a ResourceListResultProcessor.
//
// Alias for Middleware[*mcp.ListResourcesRequest, *mcp.ListResourcesResult].
type ResourceListMiddleware = Middleware[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]

// ResourceReadResultProcessor inspects or mutates a ReadResourceResult
// before it reaches the client.
//
// Alias for ResultProcessor[*mcp.ReadResourceRequest, *mcp.ReadResourceResult].
type ResourceReadResultProcessor = ResultProcessor[*mcp.ReadResourceRequest, *mcp.ReadResourceResult]

// ResourceListResultProcessor inspects or mutates a ListResourcesResult
// before it reaches the client.
//
// Alias for ResultProcessor[*mcp.ListResourcesRequest, *mcp.ListResourcesResult].
type ResourceListResultProcessor = ResultProcessor[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]

// ResourceReadErrorHandler decides how a Go error from the read chain
// reaches the client. See DefaultResourceReadErrorHandler.
//
// Alias for ErrorHandler[*mcp.ReadResourceRequest, *mcp.ReadResourceResult].
type ResourceReadErrorHandler = ErrorHandler[*mcp.ReadResourceRequest, *mcp.ReadResourceResult]

// ResourceListErrorHandler decides how a Go error from the list chain
// reaches the client. See DefaultResourceListErrorHandler.
//
// Alias for ErrorHandler[*mcp.ListResourcesRequest, *mcp.ListResourcesResult].
type ResourceListErrorHandler = ErrorHandler[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]

// WithResourceReadMiddleware appends ResourceReadMiddleware. First
// argument is outermost; nil entries panic.
func WithResourceReadMiddleware(m ...ResourceReadMiddleware) ServerOption {
	return func(s *Server) {
		for i, mw := range m {
			if mw == nil {
				panic(fmt.Sprintf("protomcp: WithResourceReadMiddleware received nil at index %d", i))
			}
		}
		s.resourceReadPipeline.middleware = append(s.resourceReadPipeline.middleware, m...)
	}
}

// WithResourceReadResultProcessor appends resource-read result
// processors. Nil entries panic.
func WithResourceReadResultProcessor(p ...ResourceReadResultProcessor) ServerOption {
	return func(s *Server) {
		for i, proc := range p {
			if proc == nil {
				panic(fmt.Sprintf("protomcp: WithResourceReadResultProcessor received nil at index %d", i))
			}
		}
		s.resourceReadPipeline.processors = append(s.resourceReadPipeline.processors, p...)
	}
}

// WithResourceReadErrorHandler replaces the resource-read error
// handler. Nil is ignored.
func WithResourceReadErrorHandler(h ResourceReadErrorHandler) ServerOption {
	return func(s *Server) {
		if h != nil {
			s.resourceReadPipeline.errHandler = h
		}
	}
}

// WithResourceListMiddleware appends ResourceListMiddleware, typically
// the middleware half of a pagination helper. Nil entries panic.
func WithResourceListMiddleware(m ...ResourceListMiddleware) ServerOption {
	return func(s *Server) {
		for i, mw := range m {
			if mw == nil {
				panic(fmt.Sprintf("protomcp: WithResourceListMiddleware received nil at index %d", i))
			}
		}
		s.resourceListPipeline.middleware = append(s.resourceListPipeline.middleware, m...)
	}
}

// WithResourceListResultProcessor appends resource-list result
// processors, typically the processor half of a pagination helper.
// Nil entries panic.
func WithResourceListResultProcessor(p ...ResourceListResultProcessor) ServerOption {
	return func(s *Server) {
		for i, proc := range p {
			if proc == nil {
				panic(fmt.Sprintf("protomcp: WithResourceListResultProcessor received nil at index %d", i))
			}
		}
		s.resourceListPipeline.processors = append(s.resourceListPipeline.processors, p...)
	}
}

// WithResourceListErrorHandler replaces the resource-list error
// handler. Nil is ignored.
func WithResourceListErrorHandler(h ResourceListErrorHandler) ServerOption {
	return func(s *Server) {
		if h != nil {
			s.resourceListPipeline.errHandler = h
		}
	}
}

// ResourceReadChain composes the configured middleware around final.
// The first registered middleware is the outermost wrapper.
func (s *Server) ResourceReadChain(final ResourceReadHandler) ResourceReadHandler {
	return s.resourceReadPipeline.chain(final)
}

// ResourceListChain is the list-surface analog of ResourceReadChain.
func (s *Server) ResourceListChain(final ResourceListHandler) ResourceListHandler {
	return s.resourceListPipeline.chain(final)
}

// HandleResourceReadError runs the configured ResourceReadErrorHandler.
// Falls back to the original error if the handler returns (nil, nil).
func (s *Server) HandleResourceReadError(ctx context.Context, req *mcp.ReadResourceRequest, err error) (*mcp.ReadResourceResult, error) {
	if err == nil {
		return nil, nil
	}
	p := &s.resourceReadPipeline
	h := p.errHandler
	if h == nil {
		h = DefaultResourceReadErrorHandler
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

// HandleResourceListError is the list-surface analog.
func (s *Server) HandleResourceListError(ctx context.Context, req *mcp.ListResourcesRequest, err error) (*mcp.ListResourcesResult, error) {
	if err == nil {
		return nil, nil
	}
	p := &s.resourceListPipeline
	h := p.errHandler
	if h == nil {
		h = DefaultResourceListErrorHandler
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

// FinishResourceRead is the wrap-up entry point for resources/read.
// Processor errors propagate as JSON-RPC errors without re-entering the
// error handler.
func (s *Server) FinishResourceRead(
	ctx context.Context,
	req *mcp.ReadResourceRequest,
	g *GRPCData,
	result *mcp.ReadResourceResult,
	err error,
) (*mcp.ReadResourceResult, error) {
	result, fErr := s.resourceReadPipeline.finish(ctx, req, g, result, err)
	if fErr != nil {
		return nil, fErr
	}
	if result == nil {
		panic(fmt.Sprintf(
			"protomcp: FinishResourceRead reached with no result and no error; "+
				"a ResourceReadMiddleware returned (nil, nil) (uri=%q)",
			resourceReadURIFromRequest(req),
		))
	}
	return result, nil
}

// FinishResourceList is the list-surface analog of FinishResourceRead.
func (s *Server) FinishResourceList(
	ctx context.Context,
	req *mcp.ListResourcesRequest,
	g *GRPCData,
	result *mcp.ListResourcesResult,
	err error,
) (*mcp.ListResourcesResult, error) {
	result, fErr := s.resourceListPipeline.finish(ctx, req, g, result, err)
	if fErr != nil {
		return nil, fErr
	}
	if result == nil {
		panic(fmt.Sprintf(
			"protomcp: FinishResourceList reached with no result and no error; "+
				"a ResourceListMiddleware returned (nil, nil) (cursor=%q)",
			resourceListCursorFromRequest(req),
		))
	}
	return result, nil
}

// DefaultResourceReadErrorHandler maps any error to a JSON-RPC protocol
// error via grpcErrorToJSONRPC; resources/read has no IsError result shape.
func DefaultResourceReadErrorHandler(_ context.Context, _ *mcp.ReadResourceRequest, err error) (*mcp.ReadResourceResult, error) {
	if err == nil {
		return nil, nil
	}
	return nil, grpcErrorToJSONRPC(err)
}

// DefaultResourceListErrorHandler is the list-surface analog.
func DefaultResourceListErrorHandler(_ context.Context, _ *mcp.ListResourcesRequest, err error) (*mcp.ListResourcesResult, error) {
	if err == nil {
		return nil, nil
	}
	return nil, grpcErrorToJSONRPC(err)
}

// resourceReadURIFromRequest returns the URI or "<unknown>".
func resourceReadURIFromRequest(req *mcp.ReadResourceRequest) string {
	if req == nil || req.Params == nil || req.Params.URI == "" {
		return "<unknown>"
	}
	return req.Params.URI
}

// resourceListCursorFromRequest returns the cursor or "<first-page>".
func resourceListCursorFromRequest(req *mcp.ListResourcesRequest) string {
	if req == nil || req.Params == nil || req.Params.Cursor == "" {
		return "<first-page>"
	}
	return req.Params.Cursor
}
