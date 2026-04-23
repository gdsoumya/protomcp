package subscriptions

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"
)

// Manager turns Hub topics into MCP resources/updated notifications.
// It tracks per-(session, URI) cancel funcs so Unsubscribe tears down
// precisely, and spawns one watchdog goroutine per session to cancel
// any leftover subscriptions if the client disconnects without calling
// Unsubscribe (a common failure mode: crashes, dropped connections,
// forgotten teardown).
//
// Manager's Subscribe and Unsubscribe methods have the exact shape the
// SDK expects for ServerOptions.SubscribeHandler / UnsubscribeHandler,
// so installation is a direct pass-through via protomcp.WithSDKOptions.
type Manager struct {
	hub      *Hub
	tmpl     *uritemplate.Template
	notifier Notifier

	mu       sync.Mutex
	subs     map[subKey]context.CancelFunc
	watching map[*mcp.ServerSession]struct{}
}

// Notifier is the callback the Manager invokes on every Hub tick. In
// production code this is always `srv.SDK().ResourceUpdated`. We keep
// it abstract here so tests can observe calls without threading a real
// server through the plumbing.
type Notifier func(ctx context.Context, params *mcp.ResourceUpdatedNotificationParams) error

type subKey struct {
	sess *mcp.ServerSession
	uri  string
}

// NewManager constructs a Manager that accepts subscribe requests for
// URIs matching the RFC 6570 single-brace template (e.g. "tasks://{id}").
// notifier is the callback invoked on every Hub tick; point it at
// `srv.SDK().ResourceUpdated` after the MCP server is constructed.
//
// Returns an error on malformed uriTemplate.
func NewManager(hub *Hub, uriTemplate string, notifier Notifier) (*Manager, error) {
	tmpl, err := uritemplate.New(uriTemplate)
	if err != nil {
		return nil, fmt.Errorf("subscriptions: parse URI template %q: %w", uriTemplate, err)
	}
	if notifier == nil {
		return nil, fmt.Errorf("subscriptions: notifier is required")
	}
	return &Manager{
		hub:      hub,
		tmpl:     tmpl,
		notifier: notifier,
		subs:     map[subKey]context.CancelFunc{},
		watching: map[*mcp.ServerSession]struct{}{},
	}, nil
}

// Subscribe is the ServerOptions.SubscribeHandler implementation. It
// validates the URI against the template, opens a Hub subscription,
// spawns a goroutine that fires the Notifier on every tick, and
// registers a session-close watchdog on the first subscribe for a
// given session.
func (m *Manager) Subscribe(_ context.Context, req *mcp.SubscribeRequest) error {
	if req == nil || req.Params == nil {
		return fmt.Errorf("subscriptions: subscribe request missing params")
	}
	uri := req.Params.URI
	if !m.tmpl.Regexp().MatchString(uri) {
		return fmt.Errorf("subscriptions: URI %q does not match template %q",
			uri, m.tmpl.Raw())
	}

	hubCh, cancelHub := m.hub.Subscribe(uri)
	watchCtx, cancelCtx := context.WithCancel(context.Background())
	cancel := func() {
		cancelCtx()
		cancelHub()
	}

	key := subKey{req.Session, uri}
	m.mu.Lock()
	// Duplicate subscribe for the same (session, URI) is legal per the MCP
	// spec, a client may re-issue subscribe during reconnect or retry.
	// Cancel the prior watcher before replacing it so the previous Hub
	// subscription + forwarder goroutine don't leak until session close.
	prior, hadPrior := m.subs[key]
	m.subs[key] = cancel
	startWatchdog := false
	if _, ok := m.watching[req.Session]; !ok {
		m.watching[req.Session] = struct{}{}
		startWatchdog = true
	}
	m.mu.Unlock()
	if hadPrior {
		prior()
	}

	// Forward every Hub tick to the MCP session as a resources/updated
	// notification. The SDK's ResourceUpdated fans out to exactly the
	// sessions that subscribed to this URI, no per-session routing
	// needed here.
	go func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case _, ok := <-hubCh:
				if !ok {
					return
				}
				_ = m.notifier(watchCtx, &mcp.ResourceUpdatedNotificationParams{URI: uri})
			}
		}
	}()

	// First subscribe for this session: start watching for session close.
	// ss.Wait() blocks until the connection drops; one watchdog covers
	// arbitrarily many subscriptions on the same session.
	if startWatchdog {
		go m.watchSession(req.Session)
	}
	return nil
}

// Unsubscribe is the ServerOptions.UnsubscribeHandler implementation.
// It cancels the matching (session, URI) subscription if present; an
// unsubscribe for an unknown key is silently ignored (the SDK spec
// permits this, clients may send duplicate unsubscribes during cleanup).
func (m *Manager) Unsubscribe(_ context.Context, req *mcp.UnsubscribeRequest) error {
	if req == nil || req.Params == nil {
		return fmt.Errorf("subscriptions: unsubscribe request missing params")
	}
	m.cancelAndRemove(subKey{req.Session, req.Params.URI})
	return nil
}

func (m *Manager) cancelAndRemove(k subKey) {
	m.mu.Lock()
	cancel, ok := m.subs[k]
	if ok {
		delete(m.subs, k)
	}
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// watchSession blocks on ss.Wait() and cancels every subscription still
// held for that session when the connection closes. This is the SDK-
// shape cleanup path for clients that drop without sending Unsubscribe
// (crashes, network drops, process kills). Without this goroutine the
// Hub subscriptions and goroutines would leak until process exit.
//
// One watchdog per session (not per subscription): the first Subscribe
// for a session starts it, subsequent subscribes reuse it. The SDK does
// not expose a per-session context or OnClose callback, so ss.Wait() is
// the only cross-transport hook available.
func (m *Manager) watchSession(ss *mcp.ServerSession) {
	_ = ss.Wait()

	m.mu.Lock()
	var cancels []context.CancelFunc
	for k, c := range m.subs {
		if k.sess == ss {
			cancels = append(cancels, c)
			delete(m.subs, k)
		}
	}
	delete(m.watching, ss)
	m.mu.Unlock()

	for _, c := range cancels {
		c()
	}
}
