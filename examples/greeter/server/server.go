// Package server implements the Greeter gRPC service used by the greeter example.
// It is completely unaware of MCP, protomcp wraps it without any code changes.
package server

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	greeterv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/greeter/v1"
)

type Server struct {
	greeterv1.UnimplementedGreeterServer
}

func New() *Server { return &Server{} }

func (s *Server) SayHello(_ context.Context, req *greeterv1.HelloRequest) (*greeterv1.HelloReply, error) {
	return &greeterv1.HelloReply{Message: fmt.Sprintf("Hello, %s!", req.GetName())}, nil
}

func (s *Server) StreamGreetings(req *greeterv1.StreamGreetingsRequest, stream greeterv1.Greeter_StreamGreetingsServer) error {
	turns := int(req.GetTurns())
	if turns <= 0 {
		turns = 1
	}
	for i := 1; i <= turns; i++ {
		if err := stream.Send(&greeterv1.HelloReply{Message: fmt.Sprintf("Turn %d: hello, %s!", i, req.GetName())}); err != nil {
			return err
		}
	}
	return nil
}

// FailWith returns a gRPC status error with the requested code. Used by
// E2E tests to exercise the DefaultErrorHandler mapping end-to-end. The
// proto field is int32 but gRPC codes are uint32; we clamp negatives
// to OK so the conversion is always safe.
func (s *Server) FailWith(_ context.Context, req *greeterv1.FailWithRequest) (*greeterv1.HelloReply, error) {
	code := req.GetCode()
	if code < 0 {
		code = 0
	}
	c := codes.Code(code)
	if c == codes.OK {
		return &greeterv1.HelloReply{Message: "ok"}, nil
	}
	msg := req.GetMessage()
	if msg == "" {
		msg = c.String()
	}
	return nil, status.Error(c, msg)
}

// EchoComplex returns a response mirroring every field of the request so
// the E2E test can assert nested/repeated/enum/map round-trip fidelity.
func (s *Server) EchoComplex(_ context.Context, req *greeterv1.EchoComplexRequest) (*greeterv1.EchoComplexResponse, error) {
	var addr *greeterv1.Address
	if req.GetAddress() != nil {
		addr = &greeterv1.Address{
			Street: req.GetAddress().GetStreet(),
			City:   req.GetAddress().GetCity(),
			Zip:    req.GetAddress().GetZip(),
		}
	}
	tags := append([]string(nil), req.GetTags()...)
	counters := make(map[string]int32, len(req.GetCounters()))
	for k, v := range req.GetCounters() {
		counters[k] = v
	}
	return &greeterv1.EchoComplexResponse{
		Name:     req.GetName(),
		Tags:     tags,
		Mood:     req.GetMood(),
		Address:  addr,
		Counters: counters,
	}, nil
}

// Slow blocks until its context is canceled. It returns a gRPC status
// error carrying the ctx.Err() so callers can observe cancellation
// propagation. The _ on the unused request parameter avoids the lint.
func (s *Server) Slow(ctx context.Context, _ *greeterv1.HelloRequest) (*greeterv1.HelloReply, error) {
	<-ctx.Done()
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return nil, status.Error(codes.DeadlineExceeded, err.Error())
	}
	return nil, status.Error(codes.Canceled, "canceled")
}

func (s *Server) Internal(_ context.Context, req *greeterv1.HelloRequest) (*greeterv1.HelloReply, error) {
	return &greeterv1.HelloReply{Message: "internal-only: " + req.GetName()}, nil
}
