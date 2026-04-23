# examples/subscriptions

Two runnable demonstrations of **user-wired resource subscriptions** on
top of protomcp-generated resources. protomcp does not codegen
subscribe handlers; the MCP Go SDK already provides everything you
need.

## Why protomcp doesn't codegen subscribe

MCP's `resources/subscribe` is a per-URI fanout: the server delivers
each `resources/updated` notification to every session subscribed to
that URI. gRPC server-streaming is the opposite shape, a per-request
stream owned by one caller. The two models do not map cleanly, and
real backends also deliver change events from places that are not
gRPC streams at all (Postgres `LISTEN/NOTIFY`, Redis/NATS pub/sub,
Kafka, CDC feeds, polling, webhooks). A codegen'd annotation would
pick a wrong default for most users.

The MCP Go SDK handles subscription mechanics itself. Supply
`SubscribeHandler` and `UnsubscribeHandler` on `mcp.ServerOptions`
(both are required; the SDK panics at `NewServer` if only one is
set), then call `srv.SDK().ResourceUpdated(ctx, params)` whenever a
resource changes. The SDK tracks per-session per-URI subscriptions
internally and fans each `ResourceUpdated` call out to only the
sessions that asked for that URI.

**The subscribe/unsubscribe handlers only act as a gate.** When a
`resources/subscribe` request arrives, the SDK calls your handler
first (`return err` to reject, `return nil` to allow); on allow, it
**unconditionally** records the session in its internal
subscriptions map. `ResourceUpdated` always reads from that same map,
so it fans out to subscribed sessions regardless of whether your
handlers are no-ops or doing real upstream work. The handler type
decides **which URIs are accepted**, not whether fan-out happens.

## Pick the pattern that matches your event source

### Pattern A: push from the write path (simpler, more common)

Supply **no-op** `SubscribeHandler` and `UnsubscribeHandler`, then
call `ResourceUpdated` directly from wherever mutations happen. The
handlers only need to return `nil` (returning an error rejects the
subscribe). This is the pattern the MCP Go SDK's own conformance
server uses.

```go
srv := protomcp.New("svc", "0.1.0",
    protomcp.WithSDKOptions(&mcp.ServerOptions{
        SubscribeHandler:   func(context.Context, *mcp.SubscribeRequest) error { return nil },
        UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
    }),
)

// From any code path that mutates a resource:
srv.SDK().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{
    URI: "tasks://" + id,
})
```

Use this when mutation events originate inside the same process as
the MCP server: a gRPC write handler, an HTTP mutation endpoint, a
background job updating shared state. No indirection, no extra
moving parts.

**Runnable demo:** [`cmd/subscriptions-simple`](cmd/subscriptions-simple). The
Tasks gRPC service's `OnChange` hook directly calls `ResourceUpdated`.
About 30 lines of wiring.

### Pattern B: wrap an external source

Use a real `SubscribeHandler` when you need to start upstream
delivery per subscription: open a gRPC stream, run a PG `LISTEN`,
subscribe to a Redis or Kafka topic, register a webhook. Your
handler does the "start feed for this URI" work; your
`UnsubscribeHandler` stops it. When events arrive from the external
source, you still call `ResourceUpdated` to fan them out: the SDK's
session-tracking behaviour is the same as Pattern A, the custom
handlers only add gating (e.g. reject unknown URIs) and lifecycle
hooks for starting / stopping upstream work.

```go
mgr, _ := subscriptions.NewManager(hub, "tasks://{id}", notifier)

srv = protomcp.New("svc", "0.1.0",
    protomcp.WithSDKOptions(&mcp.ServerOptions{
        SubscribeHandler:   mgr.Subscribe,
        UnsubscribeHandler: mgr.Unsubscribe,
    }),
)
```

Use this when the event source is external to the MCP server
process, or when you want to multiplex several external sources into
one unified view for MCP clients.

**Runnable demo:** [`cmd/subscriptions`](cmd/subscriptions). An
in-process `Hub` stands in for a pub/sub / CDC / polling source, and
`Manager` turns Hub topics into `resources/updated` notifications
with session-close cleanup.

### Combo

Dispatch inside `SubscribeHandler` on URI pattern or scheme when some
resources push from internal code and others need external
subscription. Return `nil` for the push-path URIs (no-op) and call
into your Manager for the external-source URIs.

## What Pattern B's reference code provides

- `hub.go`, a trivial in-process `Hub` standing in for a real
  backend (pub/sub client, CDC consumer, polling loop, any source
  that can publish a string topic).
- `manager.go`, a `Manager` that turns Hub topics into MCP
  `resources/updated` notifications. Its `Subscribe` and `Unsubscribe`
  methods have signatures that match the SDK's `SubscribeHandler` /
  `UnsubscribeHandler` exactly, so installation is a direct
  pass-through via `protomcp.WithSDKOptions`.
- `cmd/subscriptions/main.go`, a runnable demo.
- `e2e_test.go`, coverage of subscribe, update, unsubscribe, and
  session-close cleanup.

### Details easy to miss in Pattern B

- **Two-phase notifier wiring.** `NewManager` needs a `Notifier`
  function, but the natural implementation calls
  `srv.SDK().ResourceUpdated`, and `srv` doesn't exist yet when the
  Manager is constructed. The example resolves this with a closure
  over a `*protomcp.Server` that's populated right after
  `protomcp.New(...)` returns. Safe because the notifier is only
  invoked from goroutines spawned inside `Subscribe`, which cannot
  run before the server is listening.

- **Session-close cleanup via `ss.Wait()`.** The SDK has no
  `OnClose` callback or per-session context. `ServerSession.Wait()`
  blocks until the connection drops, so one watchdog goroutine per
  session is enough to catch "client crashed without
  unsubscribing". The example starts exactly one watchdog on the
  first Subscribe for a session; it cancels every remaining
  subscription when `Wait` returns.

- **URI template matching.** Use the same `yosida95/uritemplate/v3`
  library protomcp's generated resource code uses; `.Regexp()` gives
  the correct single-brace matcher for MCP's RFC 6570 URI templates.

## Running the demos

```shell
# Pattern A: minimal push-from-write-path
go run ./examples/subscriptions/cmd/subscriptions-simple -addr :8080

# Pattern B: external-source bridging with Hub + Manager
go run ./examples/subscriptions/cmd/subscriptions -addr :8080
```

Point any MCP client at `http://localhost:8080`, call
`resources/subscribe` with `{URI: "tasks://<id>"}`, then mutate the
task via `Tasks_UpdateTask`. A `resources/updated` notification
arrives on both variants.

## Running the tests

```shell
go test -race ./examples/subscriptions/...
```

Covers, for Pattern B: subscribe then update yields a notification;
unsubscribe stops further notifications; session close without
unsubscribe cleans up the Hub subscription.
