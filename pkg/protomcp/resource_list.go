package protomcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceLister handles the resources/list request. At most one is
// allowed per Server: MCP's resources/list is a single flat
// cursor-paginated stream, and two listers cannot share it. Listers
// registered here bypass the ResourceListMiddleware/Processor/Error
// chains unless they manually invoke ResourceListChain +
// FinishResourceList.
type ResourceLister func(ctx context.Context, req *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error)

// RegisterResourceLister installs the resources/list handler on the
// server. Panics on a second call.
func (s *Server) RegisterResourceLister(lister ResourceLister) {
	if lister == nil {
		panic("protomcp: RegisterResourceLister: nil lister")
	}
	s.listerRegMu.Lock()
	if s.lister != nil {
		s.listerRegMu.Unlock()
		panic("protomcp: RegisterResourceLister called twice; at most one " +
			"resource_list annotation per server is supported (MCP " +
			"resources/list is a single flat paginated stream)")
	}
	s.lister = lister
	s.listerRegMu.Unlock()
	s.sdk.AddReceivingMiddleware(s.resourcesListMiddleware)
}

// resourcesListMiddleware intercepts resources/list and dispatches to
// the registered lister; other methods pass through.
func (s *Server) resourcesListMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "resources/list" {
			return next(ctx, method, req)
		}
		lr, ok := req.(*mcp.ListResourcesRequest)
		if !ok {
			return next(ctx, method, req)
		}
		s.listerRegMu.Lock()
		lister := s.lister
		s.listerRegMu.Unlock()
		if lister == nil {
			return next(ctx, method, req)
		}
		return lister(ctx, lr)
	}
}
