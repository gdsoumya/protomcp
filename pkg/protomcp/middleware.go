// Package protomcp is a thin runtime glue layer on top of
// github.com/modelcontextprotocol/go-sdk/mcp. Generated code from
// protoc-gen-mcp wires concrete gRPC clients into this runtime, which
// provides the middleware chain, context propagation, transport
// selection, default error mapping, and result post-processing.
package protomcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// GRPCRequest bundles everything a Middleware can inspect or mutate
// before the upstream gRPC call is made. Collecting these fields into
// a single struct keeps the Handler signature small and, more
// importantly, lets us add new fields in the future without breaking
// every existing Middleware.
type GRPCRequest struct {
	// Input is the typed proto request the generated tool handler will
	// pass to the gRPC client. Middleware may mutate it — either by type
	// assertion to the concrete message, or generically via proto
	// reflection — and mutations persist because Input is a pointer to
	// the same value the downstream call uses:
	//
	//	if r, ok := g.Input.(*greeterv1.HelloRequest); ok {
	//	    r.TenantId = tenantFromCtx(ctx)
	//	}
	Input proto.Message

	// Metadata is the outgoing gRPC metadata that will be attached to
	// the upstream call via metadata.NewOutgoingContext. Middleware
	// appends keys; the final Handler consumes the whole MD at the
	// gRPC-call boundary.
	Metadata metadata.MD
}

// Handler is the inner step in the middleware chain. The final Handler
// (supplied by generated code) makes the upstream gRPC call using the
// accumulated GRPCRequest; each intermediate Handler is the composition
// of a Middleware wrapping the next Handler.
type Handler func(ctx context.Context, req *mcp.CallToolRequest, g *GRPCRequest) (*mcp.CallToolResult, error)

// Middleware wraps a Handler. A single Middleware can inspect the MCP
// request, read values stashed on ctx by outer HTTP middleware, mutate
// g.Input, append to g.Metadata, short-circuit with an error, or call
// next to continue the chain.
//
// Middleware is the MCP-call-level seam: it runs inside the tool handler
// after the SDK has parsed JSON-RPC. HTTP-layer concerns (authentication,
// CORS, rate limiting, access logs) belong in stdlib http.Handler
// middleware wrapped around the Server itself (Server implements
// http.Handler — see Server.ServeHTTP). Result-shape concerns
// (redaction, enrichment) may go either in a Middleware's post-call
// section (after `next(…)` returns) or in a dedicated ResultProcessor.
type Middleware func(next Handler) Handler
