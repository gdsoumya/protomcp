# tasks example

A CRUD-style Tasks service plus a lightweight Tag type. Exercises every
MCP primitive protomcp codegens today:

- every tool-hint flag (`read_only`, `idempotent`, `destructive`)
- `google.api.field_behavior = OUTPUT_ONLY` (input-schema stripping
  plus the recursive runtime clear)
- two resource templates on one server (`tasks://{id}`, `tags://{id}`)
- a single `resources/list` that enumerates *both* types via a
  `{type}://{id}`-style URI template
- a `resource_list_changed` watcher that fires
  `notifications/resources/list_changed` on every CRUD mutation
- an MCP prompt rendered from a gRPC response
- an elicitation gate on `DeleteTask`
- `@example` markers + `enumDescriptions` on `TaskStatus`

## What it demonstrates

### Tools

| Tool | Annotation hints | What it teaches |
|---|---|---|
| `Tasks_ListTasks` | `read_only: true` | safe for an LLM to call speculatively |
| `Tasks_GetTask` | `read_only: true` | read-only with a required identifier; also a `resource_template` |
| `Tasks_CreateTask` | *(none)* | OUTPUT_ONLY fields on the embedded Task (`id`, `createdAt`, `updatedAt`) are stripped from the advertised input schema AND cleared at runtime, client cannot spoof server-computed values |
| `Tasks_UpdateTask` | `idempotent: true` | writable fields modeled flat (Task.id being OUTPUT_ONLY can't carry the update target) |
| `Tasks_DeleteTask` | `idempotent: true`, `destructive: true` + elicitation | hint LLM client to confirm, with a server-enforced confirmation prompt on top |

### Resource templates

Two `protomcp.v1.resource_template` annotations, both advertised via
`resources/templates/list`, both readable via `resources/read`:

- `tasks://{id}` on `GetTask` (also a tool; the annotation only adds
  the resource surface, the CRUD tool remains)
- `tags://{id}` on `GetTag`, read-only; tags are auxiliary labels,
  not something the LLM should manipulate

### Resource list (multi-type)

One `protomcp.v1.resource_list` annotation on `ListAllResources`. The
URI template is `{type}://{id}`; each item carries its own `type`
(`"tasks"` or `"tags"`) so expansion yields `tasks://<id>` for tasks
and `tags://<id>` for tags. This is the idiomatic way to serve a
multi-type `resources/list`, MCP's flat stream has no URI filter, so
the union happens gRPC-side rather than in protomcp.

Pagination is off-the-shelf `protomcp.OffsetPagination("limit",
"offset", N)` wired on the MCP `Server`; the `ListAllResourcesRequest`
exposes the `limit` / `offset` fields the middleware stamps.

### List-changed watcher

One `protomcp.v1.resource_list_changed` annotation on the
`WatchResourceChanges` server-streaming RPC. The tasks server
broadcasts a bare tick to every WatchResourceChanges subscriber on
each CRUD mutation; the generated watcher forwards each tick to
`srv.NotifyResourceListChanged()`, which fans out
`notifications/resources/list_changed` to every MCP session. The SDK
debounces bursts in a ~10ms window so a flurry of writes collapses
into one wire notification.

Lifecycle: call `StartTasksMCPResourceListChangedWatchers(ctx, srv,
grpcClient)` after `RegisterTasksMCPResources` with a ctx bound to
your process lifetime (typically a `signal.NotifyContext`). The
watcher exits cleanly when ctx is canceled.

### Prompt

`tasks_review` renders a user-role prompt message from a gRPC response
via a Mustache template, see `TaskReview` in the .proto.

### Elicitation

`DeleteTask` is gated on a confirmation prompt (`protomcp.v1.elicitation`
with a Mustache-rendered message). MCP clients surface the prompt to
the user before the gRPC call runs; `decline` / `cancel` short-circuit
with an `IsError` result.

## Run it

```bash
# Starts the Tasks gRPC server in-process and fronts it with protomcp
# on 127.0.0.1:8080. Seeds three tags at boot so resources/list has
# content to enumerate.
go run ./examples/tasks/cmd/tasks

# or pick a port / page size:
go run ./examples/tasks/cmd/tasks -addr :9000 -page-size 5
```

Connect any MCP client to `http://127.0.0.1:8080/`. The server
advertises five tools, two resource templates, one prompt, and a
`resources/list` covering tasks + tags.

## Try it with the SDK test client (no external MCP client needed)

```bash
go test -race -count=1 ./examples/tasks/...
```

Covers the full surface:

- `e2e_test.go`, CRUD round-trip, hints, input-schema stripping, elicitation accept path, NOT_FOUND error mapping
- `e2e_resources_test.go`, multi-type `resources/list` with `OffsetPagination`, `resource_template` reads for both `tasks://{id}` and `tags://{id}`, `resource_list_changed` fires on CRUD + debounces bursts
- `e2e_prompts_test.go`, `tasks_review` Mustache render
- `e2e_elicitation_test.go`, accept + decline behaviour

## Files

```
proto/examples/tasks/v1/tasks.proto    # annotated CRUD + list service
examples/tasks/server/server.go        # in-memory gRPC implementation
examples/tasks/cmd/tasks/main.go       # runnable binary (seeds tags)
examples/tasks/e2e_test.go             # CRUD / hints / elicitation
examples/tasks/e2e_resources_test.go   # templates + multi-type list
examples/tasks/e2e_prompts_test.go     # prompt rendering
examples/tasks/e2e_elicitation_test.go # elicitation accept/decline
```
