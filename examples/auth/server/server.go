// Package server implements the Profile gRPC service used by the auth example.
// It has no MCP awareness; a UnaryServerInterceptor reads the gRPC metadata
// that an MCP-side middleware wrote. This is the acceptance test that proves
// context propagation works end-to-end.
package server

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/auth/v1"
)

// principalKey is a context value populated by the UnaryServerInterceptor
// after reading metadata. The handler reads it back out.
type principalKey struct{}

type principal struct {
	UserID string
	Tenant string
}

// PrincipalInterceptor extracts x-user-id and x-tenant from incoming gRPC
// metadata and stashes them as a principal on the context. Rejects calls
// that lack x-user-id.
func PrincipalInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		user := first(md.Get("x-user-id"))
		if user == "" {
			return nil, status.Error(codes.Unauthenticated, "missing x-user-id")
		}
		ctx = context.WithValue(ctx, principalKey{}, &principal{
			UserID: user,
			Tenant: first(md.Get("x-tenant")),
		})
		return handler(ctx, req)
	}
}

type Server struct {
	authv1.UnimplementedProfileServer
}

func New() *Server { return &Server{} }

func (s *Server) WhoAmI(ctx context.Context, _ *authv1.WhoAmIRequest) (*authv1.WhoAmIResponse, error) {
	p, ok := ctx.Value(principalKey{}).(*principal)
	if !ok {
		return nil, status.Error(codes.Internal, "principal missing (interceptor bug)")
	}
	if p.UserID == "" {
		return nil, fmt.Errorf("empty principal")
	}
	return &authv1.WhoAmIResponse{UserId: p.UserID, Tenant: p.Tenant}, nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
