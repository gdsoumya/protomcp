// Package server implements the Tasks gRPC service used by the tasks
// example. It is an in-memory CRUD store with no persistence and no
// awareness of MCP, protomcp wraps it without any code changes.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	tasksv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/tasks/v1"
)

// sortStrings keeps the List/Pagination output deterministic. Factored
// out so tests can rely on stable slice boundaries.
func sortStrings(s []string) { sort.Strings(s) }

// Server is an in-memory implementation of the Tasks service. Safe for
// concurrent use: every access goes through mu.
type Server struct {
	tasksv1.UnimplementedTasksServer

	mu    sync.Mutex
	tasks map[string]*tasksv1.Task
	tags  map[string]*tasksv1.Tag
	now   func() time.Time // injectable for tests; defaults to time.Now

	// OnChange is invoked (if non-nil) after each successful write with
	// the task id that changed. The subscriptions example uses this hook
	// to drive a user-written SubscribeHandler; production code would
	// plug in a real CDC feed or message bus at the service boundary.
	OnChange func(id string)

	// changeSubs fan-out set for WatchResourceChanges subscribers.
	// Populated on each gRPC stream open; each CRUD mutation sends a
	// non-blocking tick to every entry. Slow consumers drop ticks
	// rather than block mutations, the MCP list_changed contract is
	// "eventually consistent" so coalescing is acceptable.
	changesMu  sync.Mutex
	changeSubs map[chan struct{}]struct{}
}

func New() *Server {
	return &Server{
		tasks:      map[string]*tasksv1.Task{},
		tags:       map[string]*tasksv1.Tag{},
		now:        time.Now,
		changeSubs: map[chan struct{}]struct{}{},
	}
}

// subscribeChanges returns a channel that receives a tick on every
// CRUD mutation, plus an unsubscribe func. Buffered at 8; slow
// subscribers miss ticks (the list_changed contract tolerates this).
func (s *Server) subscribeChanges() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 8)
	s.changesMu.Lock()
	s.changeSubs[ch] = struct{}{}
	s.changesMu.Unlock()
	return ch, func() {
		s.changesMu.Lock()
		defer s.changesMu.Unlock()
		if _, ok := s.changeSubs[ch]; ok {
			delete(s.changeSubs, ch)
			close(ch)
		}
	}
}

// broadcastChange sends a non-blocking tick to every registered
// subscriber. Called from every CRUD mutation path.
func (s *Server) broadcastChange() {
	s.changesMu.Lock()
	chans := make([]chan struct{}, 0, len(s.changeSubs))
	for ch := range s.changeSubs {
		chans = append(chans, ch)
	}
	s.changesMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// SeedTag inserts a tag with a deterministic id at startup. The tasks
// example seeds a handful at boot so the multi-type resource_list demo
// has something to enumerate on a fresh server. Real services would
// populate tags from a backing store.
func (s *Server) SeedTag(name string) *tasksv1.Tag {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &tasksv1.Tag{Id: newID(), Name: name}
	s.tags[t.Id] = t
	return t
}

// emitChange fires OnChange (if configured) AND broadcasts a bare
// tick to every WatchResourceChanges subscriber, both without holding
// s.mu, the hook may do arbitrary user work (enqueue, log,
// synchronously notify) and must not deadlock with concurrent writes.
func (s *Server) emitChange(id string) {
	if s.OnChange != nil {
		s.OnChange(id)
	}
	s.broadcastChange()
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
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sortStrings(ids)
	out := make([]*tasksv1.Task, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneTask(s.tasks[id]))
	}
	return &tasksv1.ListTasksResponse{Tasks: out}, nil
}

func (s *Server) GetTag(_ context.Context, req *tasksv1.GetTagRequest) (*tasksv1.Tag, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tags[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "tag %q not found", req.GetId())
	}
	c, _ := proto.Clone(t).(*tasksv1.Tag)
	return c, nil
}

// WatchResourceChanges is the server-streaming feed behind the
// `protomcp.v1.resource_list_changed` annotation. It emits one bare
// event per CRUD mutation; protomcp's generated watcher reads these
// and fires srv.NotifyResourceListChanged() per event.
//
// Event payloads are intentionally empty, MCP's list_changed signal
// is a trigger, not a delta stream. Clients are expected to re-call
// `resources/list` when they see the notification.
func (s *Server) WatchResourceChanges(_ *tasksv1.WatchResourceChangesRequest, stream grpc.ServerStreamingServer[tasksv1.WatchResourceChangesEvent]) error {
	ch, unsub := s.subscribeChanges()
	defer unsub()
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(&tasksv1.WatchResourceChangesEvent{}); err != nil {
				return err
			}
		}
	}
}

// ListAllResources is the single resources/list endpoint. It walks
// tasks first (id-sorted) then tags (id-sorted) and returns the window
// [offset, offset+limit) of that union. protomcp.OffsetPagination
// stamps limit+offset before calling us; we don't have to care what
// the MCP cursor looks like.
func (s *Server) ListAllResources(_ context.Context, req *tasksv1.ListAllResourcesRequest) (*tasksv1.ListAllResourcesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := make([]*tasksv1.ResourceItem, 0, len(s.tasks)+len(s.tags))
	taskIDs := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		taskIDs = append(taskIDs, id)
	}
	sortStrings(taskIDs)
	for _, id := range taskIDs {
		t := s.tasks[id]
		all = append(all, &tasksv1.ResourceItem{
			Type:        "tasks",
			Id:          t.GetId(),
			Name:        t.GetTitle(),
			Description: t.GetDescription(),
		})
	}
	tagIDs := make([]string, 0, len(s.tags))
	for id := range s.tags {
		tagIDs = append(tagIDs, id)
	}
	sortStrings(tagIDs)
	for _, id := range tagIDs {
		t := s.tags[id]
		all = append(all, &tasksv1.ResourceItem{
			Type: "tags",
			Id:   t.GetId(),
			Name: t.GetName(),
		})
	}

	offset := int(req.GetOffset())
	if offset > len(all) {
		offset = len(all)
	}
	end := len(all)
	if limit := int(req.GetLimit()); limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return &tasksv1.ListAllResourcesResponse{Items: all[offset:end]}, nil
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
	t := s.stamp(in, true)
	t.Id = newID()
	s.tasks[t.Id] = t
	s.mu.Unlock()
	s.emitChange(t.Id)
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
	existing, ok := s.tasks[req.GetId()]
	if !ok {
		s.mu.Unlock()
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
	s.mu.Unlock()
	s.emitChange(updated.Id)
	return cloneTask(updated), nil
}

// TaskReview is a thin pass-through used by the Tasks_TaskReview MCP
// prompt. The prompt wiring (template rendering, PromptMessage assembly)
// lives entirely in generated code; the server's job is just to load the
// task by id and return it so the template has something to interpolate.
func (s *Server) TaskReview(ctx context.Context, req *tasksv1.TaskReviewRequest) (*tasksv1.Task, error) {
	return s.GetTask(ctx, &tasksv1.GetTaskRequest{Id: req.GetId()})
}

func (s *Server) DeleteTask(_ context.Context, req *tasksv1.DeleteTaskRequest) (*tasksv1.DeleteTaskResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	s.mu.Lock()
	_, existed := s.tasks[req.GetId()]
	delete(s.tasks, req.GetId())
	s.mu.Unlock()
	if existed {
		s.emitChange(req.GetId())
	}
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
