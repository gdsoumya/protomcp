package protomcp

import (
	"context"
	"reflect"
)

// Handler is the inner step in a primitive's middleware chain. The
// final Handler (generator-supplied) makes the upstream gRPC call.
type Handler[Req, Res any] func(ctx context.Context, req Req, g *GRPCData) (Res, error)

// Middleware wraps a Handler.
type Middleware[Req, Res any] func(next Handler[Req, Res]) Handler[Req, Res]

// ResultProcessor observes or mutates the result before it reaches the
// MCP client. Returning a nil result is a passthrough; processors run
// in registration order after the ErrorHandler.
type ResultProcessor[Req, Res any] func(ctx context.Context, grpc *GRPCData, mcp *MCPData[Req, Res]) (Res, error)

// ErrorHandler decides how a Go error reaches the client. Returning
// (nil, nil) causes finish to fall back to the original err.
type ErrorHandler[Req, Res any] func(ctx context.Context, req Req, err error) (Res, error)

// pipeline is the shared machinery behind every primitive's chain.
type pipeline[Req, Res any] struct {
	middleware []Middleware[Req, Res]
	processors []ResultProcessor[Req, Res]
	errHandler ErrorHandler[Req, Res]
}

// chain composes the configured middleware around final. The first
// registered middleware is the outermost wrapper.
func (p *pipeline[Req, Res]) chain(final Handler[Req, Res]) Handler[Req, Res] {
	for i := len(p.middleware) - 1; i >= 0; i-- {
		final = p.middleware[i](final)
	}
	return final
}

// handleError routes err through the configured ErrorHandler, falling
// back to (zero, err) when no handler is set.
func (p *pipeline[Req, Res]) handleError(ctx context.Context, req Req, err error) (Res, error) {
	if err == nil {
		var zero Res
		return zero, nil
	}
	if p.errHandler == nil {
		var zero Res
		return zero, err
	}
	return p.errHandler(ctx, req, err)
}

// finish is the shared wrap-up every primitive's FinishX delegates to.
// Routes err through the error handler, falls back to the original err
// if the handler returns (nil, nil), then runs processors. g may be
// nil, so processors must tolerate it. On (nil result, nil err) finish
// returns (zero, nil) and leaves panicking to the caller, which knows
// the primitive name.
func (p *pipeline[Req, Res]) finish(ctx context.Context, req Req, g *GRPCData, result Res, err error) (Res, error) {
	var zero Res
	if err != nil {
		handled, perr := p.handleError(ctx, req, err)
		if perr != nil {
			return zero, perr
		}
		if isNilResult(handled) {
			return zero, err
		}
		result = handled
	}
	if len(p.processors) == 0 {
		return result, nil
	}
	data := &MCPData[Req, Res]{Input: req, Output: result}
	for _, proc := range p.processors {
		next, pErr := proc(ctx, g, data)
		if pErr != nil {
			return zero, pErr
		}
		if !isNilResult(next) {
			data.Output = next
		}
	}
	return data.Output, nil
}

// isNilResult reports whether r is a typed-nil pointer, catching the
// typed-nil-wrapped-in-interface case that `any(r) == nil` misses.
func isNilResult[Res any](r Res) bool {
	v := reflect.ValueOf(r)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
