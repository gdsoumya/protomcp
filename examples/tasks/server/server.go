// Package server implements the Tasks gRPC service used by the tasks
// example. It is an in-memory CRUD store with no persistence and no
// awareness of MCP — protomcp wraps it without any code changes.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
)

// Server is an in-memory implementation of the Tasks service. Safe for
// concurrent use: every access goes through mu.
type Server struct {
	tasksv1.UnimplementedTasksServer

	mu    sync.Mutex
	tasks map[string]*tasksv1.Task
	now   func() time.Time // injectable for tests; defaults to time.Now
}

func New() *Server {
	return &Server{tasks: map[string]*tasksv1.Task{}, now: time.Now}
}

// newID returns a short random hex id. Collisions are astronomically
// unlikely for the example store's size; a production service would use
// ULIDs or UUIDs with a monotonic clock component.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// stamp clones t with server-assigned timestamps refreshed.
func (s *Server) stamp(t *tasksv1.Task, isCreate bool) *tasksv1.Task {
	now := timestamppb.New(s.now())
	out := &tasksv1.Task{
		Id:          t.GetId(),
		Title:       t.GetTitle(),
		Description: t.GetDescription(),
		Done:        t.GetDone(),
		UpdatedAt:   now,
	}
	if isCreate {
		out.CreatedAt = now
	} else {
		out.CreatedAt = t.GetCreatedAt()
	}
	return out
}

func (s *Server) ListTasks(_ context.Context, _ *tasksv1.ListTasksRequest) (*tasksv1.ListTasksResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*tasksv1.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, cloneTask(t))
	}
	return &tasksv1.ListTasksResponse{Tasks: out}, nil
}

func (s *Server) GetTask(_ context.Context, req *tasksv1.GetTaskRequest) (*tasksv1.Task, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "task %q not found", req.GetId())
	}
	return cloneTask(t), nil
}

func (s *Server) CreateTask(_ context.Context, req *tasksv1.CreateTaskRequest) (*tasksv1.Task, error) {
	in := req.GetTask()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}
	if in.GetTitle() == "" {
		return nil, status.Error(codes.InvalidArgument, "task.title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.stamp(in, true)
	t.Id = newID()
	s.tasks[t.Id] = t
	return cloneTask(t), nil
}

func (s *Server) UpdateTask(_ context.Context, req *tasksv1.UpdateTaskRequest) (*tasksv1.Task, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required for update")
	}
	if req.GetTitle() == "" {
		return nil, status.Error(codes.InvalidArgument, "title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.tasks[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "task %q not found", req.GetId())
	}
	updated := s.stamp(&tasksv1.Task{
		Id:          req.GetId(),
		Title:       req.GetTitle(),
		Description: req.GetDescription(),
		Done:        req.GetDone(),
	}, false)
	updated.CreatedAt = existing.GetCreatedAt()
	s.tasks[updated.Id] = updated
	return cloneTask(updated), nil
}

func (s *Server) DeleteTask(_ context.Context, req *tasksv1.DeleteTaskRequest) (*tasksv1.DeleteTaskResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.tasks[req.GetId()]
	delete(s.tasks, req.GetId())
	return &tasksv1.DeleteTaskResponse{Existed: existed}, nil
}

// cloneTask returns a deep copy so mutations by the caller do not bleed
// back into the store. proto.Clone handles the internal Mutex that
// protoimpl.MessageState embeds in every generated message; the type
// assertion cannot fail since we pass a *Task in.
func cloneTask(t *tasksv1.Task) *tasksv1.Task {
	if t == nil {
		return nil
	}
	c, _ := proto.Clone(t).(*tasksv1.Task)
	return c
}
