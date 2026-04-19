# tasks example

A CRUD-style Tasks service. Exercises every annotation hint (`read_only`, `idempotent`, `destructive`) and `google.api.field_behavior = OUTPUT_ONLY` on nested message fields.

## What it demonstrates

| Tool | Annotation hints | What it teaches |
|---|---|---|
| `Tasks_ListTasks` | `read_only: true` | safe for an LLM to call speculatively |
| `Tasks_GetTask` | `read_only: true` | read-only with a required identifier |
| `Tasks_CreateTask` | *(none)* | OUTPUT_ONLY fields on the embedded Task (`id`, `createdAt`, `updatedAt`) are stripped from the advertised input schema AND cleared at runtime — client cannot spoof server-computed values |
| `Tasks_UpdateTask` | `idempotent: true` | writable fields modeled flat (Task.id being OUTPUT_ONLY can't carry the update target) |
| `Tasks_DeleteTask` | `idempotent: true`, `destructive: true` | hint LLM client to confirm before calling, and that retry is safe |

## Run it

```bash
# Starts the Tasks gRPC server in-process and fronts it with protomcp
# on 127.0.0.1:8080.
go run ./examples/tasks/cmd/tasks

# or pick a port:
go run ./examples/tasks/cmd/tasks -addr :9000
```

Connect any MCP client to `http://127.0.0.1:8080/`. The server advertises five tools; `tools/list` shows each one's `annotations` block so the client can tell `DeleteTask` is destructive.

## Try it with the SDK test client (no external MCP client needed)

```bash
go test -race -count=1 ./examples/tasks/...
```

`e2e_test.go` drives the whole CRUD surface through the SDK's in-memory transport:

- `TestToolsListHints` — every hint set on the right tool
- `TestInputSchemaStripsOutputOnly` — `id` / `createdAt` / `updatedAt` absent from Create's input schema
- `TestCRUDRoundTrip` — Create → List → Get → Update → Delete (twice; idempotent)
- `TestCreateIgnoresClientSuppliedOutputOnly` — defense-in-depth: forged id on the wire is zeroed before it reaches gRPC
- `TestGetNotFound` — gRPC NOT_FOUND surfaces as `CallToolResult{IsError: true}`

## Files

```
proto/examples/tasks/v1/tasks.proto  # annotated CRUD service
examples/tasks/server/server.go      # in-memory gRPC implementation
examples/tasks/cmd/tasks/main.go     # runnable binary
examples/tasks/e2e_test.go           # MCP-client-driven CRUD tests
```
