package protomcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestResourceReadChain_MiddlewareOrder, outermost first, matches the
// tool surface. Registering [a, b] runs a-pre, b-pre, final, b-post,
// a-post, so the log of strings ends up as "a-b-f-b-a".
func TestResourceReadChain_MiddlewareOrder(t *testing.T) {
	var trace []string
	mk := func(name string) ResourceReadMiddleware {
		return func(next ResourceReadHandler) ResourceReadHandler {
			return func(ctx context.Context, req *mcp.ReadResourceRequest, g *GRPCData) (*mcp.ReadResourceResult, error) {
				trace = append(trace, name+"-pre")
				r, err := next(ctx, req, g)
				trace = append(trace, name+"-post")
				return r, err
			}
		}
	}
	s := New("t", "0.0.1",
		WithResourceReadMiddleware(mk("a"), mk("b")))
	h := s.ResourceReadChain(func(context.Context, *mcp.ReadResourceRequest, *GRPCData) (*mcp.ReadResourceResult, error) {
		trace = append(trace, "final")
		return &mcp.ReadResourceResult{}, nil
	})
	if _, err := h(context.Background(), &mcp.ReadResourceRequest{}, &GRPCData{}); err != nil {
		t.Fatalf("chain error: %v", err)
	}
	got := strings.Join(trace, ",")
	want := "a-pre,b-pre,final,b-post,a-post"
	if got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

// TestFinishResourceRead_FallbackOnNilNilFromHandler, a custom
// ResourceReadErrorHandler that returns (nil, nil) falls back to the
// original error rather than panicking. Parallel to the tool / prompt
// finishers.
func TestFinishResourceRead_FallbackOnNilNilFromHandler(t *testing.T) {
	buggy := func(_ context.Context, _ *mcp.ReadResourceRequest, _ error) (*mcp.ReadResourceResult, error) {
		return nil, nil
	}
	sentinel := errors.New("read-boom")
	s := New("t", "0.0.1", WithResourceReadErrorHandler(buggy))
	res, err := s.FinishResourceRead(context.Background(), &mcp.ReadResourceRequest{}, nil, nil, sentinel)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want original sentinel", err)
	}
}

// TestFinishResourceList_FallbackOnNilNilFromHandler, list-surface
// analog of the read-surface fall-back test.
func TestFinishResourceList_FallbackOnNilNilFromHandler(t *testing.T) {
	buggy := func(_ context.Context, _ *mcp.ListResourcesRequest, _ error) (*mcp.ListResourcesResult, error) {
		return nil, nil
	}
	sentinel := errors.New("list-boom")
	s := New("t", "0.0.1", WithResourceListErrorHandler(buggy))
	res, err := s.FinishResourceList(context.Background(), &mcp.ListResourcesRequest{}, nil, nil, sentinel)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want original sentinel", err)
	}
}

// TestFinishResourceRead_ErrorHandler, a gRPC status surface becomes a
// *jsonrpc.Error. This is the read-side analog of the tool surface's
// default error mapping.
func TestFinishResourceRead_ErrorHandler(t *testing.T) {
	s := New("t", "0.0.1")
	req := &mcp.ReadResourceRequest{}
	grpcErr := status.Error(codes.PermissionDenied, "nope")
	_, err := s.FinishResourceRead(context.Background(), req, nil, nil, grpcErr)
	var je *jsonrpc.Error
	if !errors.As(err, &je) {
		t.Fatalf("expected *jsonrpc.Error, got %T: %v", err, err)
	}
	if !strings.Contains(je.Message, "PermissionDenied") {
		t.Errorf("message = %q; want PermissionDenied substring", je.Message)
	}
}

// TestFinishResourceList_ProcessorChain, processors see the result in
// registration order and can mutate NextCursor.
func TestFinishResourceList_ProcessorChain(t *testing.T) {
	s := New("t", "0.0.1",
		WithResourceListResultProcessor(
			func(_ context.Context, _ *GRPCData, data *MCPData[*mcp.ListResourcesRequest, *mcp.ListResourcesResult]) (*mcp.ListResourcesResult, error) {
				data.Output.NextCursor = "set-by-processor"
				return data.Output, nil
			},
		),
	)
	got, err := s.FinishResourceList(context.Background(), &mcp.ListResourcesRequest{}, nil, &mcp.ListResourcesResult{}, nil)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got.NextCursor != "set-by-processor" {
		t.Errorf("NextCursor = %q, want processor value", got.NextCursor)
	}
}

// TestWithResourceReadMiddleware_NilPanics, surfaces nil middleware at
// New-time, matching WithToolMiddleware's contract.
func TestWithResourceReadMiddleware_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithResourceReadMiddleware received nil") {
			t.Fatalf("panic = %v; want nil-middleware message", r)
		}
	}()
	_ = New("t", "0.0.1", WithResourceReadMiddleware(nil))
}

// TestWithResourceListResultProcessor_NilPanics, list-surface analog.
func TestWithResourceListResultProcessor_NilPanics(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "WithResourceListResultProcessor received nil") {
			t.Fatalf("panic = %v; want nil-processor message", r)
		}
	}()
	_ = New("t", "0.0.1", WithResourceListResultProcessor(nil))
}

// TestResourcesListMiddleware_Dispatches, the registered lister is
// invoked for resources/list and its result is returned verbatim. The
// SDK default is NOT consulted when a lister is present.
func TestResourcesListMiddleware_Dispatches(t *testing.T) {
	s := New("t", "0.0.1")
	s.RegisterResourceLister(func(_ context.Context, _ *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
		return &mcp.ListResourcesResult{
			Resources: []*mcp.Resource{{URI: "a://1", Name: "a"}},
		}, nil
	})

	handler := s.resourcesListMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		t.Fatalf("SDK default handler must not fire when a lister is registered")
		return nil, nil
	})
	res, err := handler(context.Background(), "resources/list", &mcp.ListResourcesRequest{Params: &mcp.ListResourcesParams{}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	lr, ok := res.(*mcp.ListResourcesResult)
	if !ok {
		t.Fatalf("result = %T, want *mcp.ListResourcesResult", res)
	}
	if len(lr.Resources) != 1 || lr.Resources[0].URI != "a://1" {
		t.Errorf("got resources %+v, want [a://1]", lr.Resources)
	}
}

// TestRegisterResourceLister_SecondPanics, a second RegisterResourceLister
// call is a wiring mistake (MCP resources/list is a single flat stream ,
// cannot be multiplexed across listers at runtime). Panic loudly so
// the author sees it immediately.
func TestRegisterResourceLister_SecondPanics(t *testing.T) {
	s := New("t", "0.0.1")
	s.RegisterResourceLister(func(_ context.Context, _ *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
		return &mcp.ListResourcesResult{}, nil
	})
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "RegisterResourceLister called twice") {
			t.Fatalf("panic = %v; want second-call guard", r)
		}
	}()
	s.RegisterResourceLister(func(_ context.Context, _ *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
		return &mcp.ListResourcesResult{}, nil
	})
}

// TestResourcesListMiddleware_ErrorPropagates, a lister error is
// surfaced verbatim; no fallthrough to the SDK default.
func TestResourcesListMiddleware_ErrorPropagates(t *testing.T) {
	s := New("t", "0.0.1")
	s.RegisterResourceLister(func(_ context.Context, _ *mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
		return nil, errors.New("backend blew up")
	})

	handler := s.resourcesListMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		t.Fatalf("fallthrough must not fire on error")
		return nil, nil
	})
	_, err := handler(context.Background(), "resources/list", &mcp.ListResourcesRequest{Params: &mcp.ListResourcesParams{}})
	if err == nil {
		t.Fatal("err = nil, want backend error")
	}
}

// TestResourceListChain_MiddlewareOrder, the first ResourceListMiddleware
// registered is the outermost wrapper. Mirrors TestResourceReadChain's
// contract, so the list-surface chain is deterministic too.
func TestResourceListChain_MiddlewareOrder(t *testing.T) {
	var order []string
	mk := func(name string) ResourceListMiddleware {
		return func(next ResourceListHandler) ResourceListHandler {
			return func(ctx context.Context, req *mcp.ListResourcesRequest, g *GRPCData) (*mcp.ListResourcesResult, error) {
				order = append(order, name+"-pre")
				res, err := next(ctx, req, g)
				order = append(order, name+"-post")
				return res, err
			}
		}
	}
	s := New("t", "0.0.1",
		WithResourceListMiddleware(mk("a")),
		WithResourceListMiddleware(mk("b")),
	)
	final := ResourceListHandler(func(context.Context, *mcp.ListResourcesRequest, *GRPCData) (*mcp.ListResourcesResult, error) {
		order = append(order, "final")
		return &mcp.ListResourcesResult{}, nil
	})
	_, _ = s.ResourceListChain(final)(context.Background(), &mcp.ListResourcesRequest{}, &GRPCData{})
	got := strings.Join(order, ",")
	want := "a-pre,b-pre,final,b-post,a-post"
	if got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}
