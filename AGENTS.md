# AGENTS.md

Guidance for AI coding agents (Claude Code, Cursor, Copilot, aider, etc.) working in this repository. Human contributors should read `CONTRIBUTING.md` instead; this file is the **agent-facing summary** of the same rules, plus a few things that trip agents specifically.

## What this repo is

`protomcp` is a Go library that exposes any gRPC service as an [MCP (Model Context Protocol)](https://modelcontextprotocol.io) server by generating tool handlers from proto annotations. It is a **thin wrapper** over the official `modelcontextprotocol/go-sdk`; we do not reimplement the MCP protocol.

Two moving parts:

| Part | Path | Role |
|---|---|---|
| Protoc plugin | `cmd/protoc-gen-mcp/`, `internal/gen/` | reads `.proto` → emits `*.mcp.pb.go` |
| Runtime | `pkg/protomcp/` | glue over the SDK: Middleware, ErrorHandler, ResultProcessor, http.Handler |

Everything else is examples (`examples/`) and generated code (`pkg/api/gen/`).

## Non-negotiable rules

1. **Don't reimplement MCP protocol.** Everything JSON-RPC / session / progress-SSE / capability negotiation goes through the MCP Go SDK. If a change would require hand-rolling protocol logic, stop and ask.
2. **Annotation-required rendering.** An RPC is only exposed when it carries one of `protomcp.v1.tool`, `resource_template`, `resource_list`, `resource_list_changed`, or `prompt` (elicitation is a modifier that requires a sibling `tool`). Do not add an auto-expose mode or allowlist.
3. **Default-deny for OUTPUT_ONLY.** `ClearOutputOnly` runs after `protojson.Unmarshal` and recursively scrubs nested/repeated/map OUTPUT_ONLY fields. Do not remove, relax to top-level-only, or conditionalize it.
4. **Generator output is deterministic.** No timestamps, no random IDs, no map-iteration-order leakage. `json.Marshal` is the blessed serializer (it sorts keys).
5. **No product-specific integrations.** This is a generic library. Examples stay minimal.
6. **Streaming rules are primitive-specific.**
   - `tool`: unary or server-streaming. Client / bidi are hard generation errors.
   - `resource_template`, `resource_list`, `prompt`, `elicitation`: unary only. Any streaming annotation is a hard error.
   - `resource_list_changed`: server-streaming is **required**; unary / client / bidi are hard errors.
   Do not add fallbacks.
7. **At most one `resource_list` and one `resource_list_changed` per generation run.** A second annotation anywhere in the codegen set is a hard error citing both sites. The MCP spec has no URI filter for `resources/list`, so multi-type enumeration goes through a single cumulative RPC with a templated scheme (see `examples/tasks` for `{type}://{id}`).
8. **Never print to stdout from the generator.** stdout is the protoc wire channel; any stray write corrupts codegen.

## What to check before declaring a task done

Agents frequently claim "done" after the typechecker accepts the code. That is insufficient here. For any non-trivial change:

```bash
go install ./cmd/protoc-gen-mcp    # rebuild plugin if you touched generation
make gen                            # regenerate; must leave no diff
go test -race -count=1 ./...        # must pass
golangci-lint run --timeout 5m ./... # must be clean
```

LSP diagnostics go stale after `buf generate`; trust `go build` / `go test` over the IDE.

## Common traps

- **Stale generated code.** After editing any `.proto` or anything under `internal/gen/`, you must run `go install ./cmd/protoc-gen-mcp` then `buf generate` (or `make gen`). The tests in `examples/` import the generated packages; forgetting to regenerate gives confusing "undefined" errors.
- **Import cycles in tests.** `fieldbehavior_test.go` uses `package protomcp_test` because it imports `authv1` which imports `protomcp`. Keep that pattern for any test that needs a generated fixture message.
- **`json.RawMessage` as SDK Out.** The SDK treats typed-nil `json.RawMessage` as non-nil and validates `"null"` against the output schema. The generator uses `Out=any` precisely to dodge this; don't switch back to `json.RawMessage`.
- **proto message copy-locks.** Generated message types embed `protoimpl.MessageState` which contains a `sync.Mutex`. Don't struct-copy them (`c := *t`); use `proto.Clone`.
- **`paths=source_relative`.** Our `buf.gen.yaml` uses `paths=source_relative`, meaning the output directory mirrors the proto source tree. Do not change without updating every `go_package` option and every import path.
- **protojson vs json.** MCP tool content is protojson. Use `protojson.Unmarshal` in tests that decode into generated proto types, plain `json.Unmarshal` breaks on Timestamp, Duration, enum-as-name, and int64-as-string.

## Where design decisions live

If you want to understand *why* a piece of code is the way it is, look here before changing it:

| Decision | Where it's justified |
|---|---|
| Fork-free SDK usage | `pkg/protomcp/server.go` (`SDK()` method SECURITY block) |
| OUTPUT_ONLY recursion | `pkg/protomcp/fieldbehavior.go` (doc comment on `ClearOutputOnly`) |
| Middleware ordering (outermost-first) | `pkg/protomcp/pipeline.go` (doc comment on `pipeline.chain`) |
| Per-primitive typed pipelines | `pkg/protomcp/pipeline.go` (doc comment on `pipeline`) |
| `ResultProcessor` receives `*GRPCData` + `*MCPData` | `pkg/protomcp/pipeline.go` (doc comment on `ResultProcessor`) |
| `FinishToolCall` nil-Out on IsError | `pkg/protomcp/server.go` (doc comment on `FinishToolCall`) |
| `NotifyResourceListChanged` sentinel add/remove | `pkg/protomcp/list_changed.go` (doc comment on `NotifyResourceListChanged`) |
| Single `ResourceLister` per server | `pkg/protomcp/resource_list.go` (doc comment on `RegisterResourceLister`) |
| Cross-file tool-name / resource_list / list_changed uniqueness | `internal/gen/generator.go` (comments on `emitted`, `resourceListSite`, `resourceListChangedSite`) |
| Tool-name validation at codegen | `internal/gen/generator.go` (`validateToolName`) |
| `OffsetPagination` vs `PageTokenPagination` split | `pkg/protomcp/pagination.go` (function docs) |
| int64/uint64 → string JSON | `internal/gen/schema/schema.go` (`kindToJSONType`) |
| bytes `min_len`/`max_len` advisory caveat | `internal/gen/schema/schema.go` (comment on `applyInt64Rules`) |

## Adding a feature

1. Write or update proto in `proto/`.
2. Wire it through `pkg/protomcp/` if runtime-visible (new option, type, or helper).
3. Wire it through `internal/gen/` if generator-visible (template changes live in `internal/gen/templates/*.tmpl`; per-primitive builders in `generator.go`, `resources.go`, `prompt.go`).
4. Rebuild the plugin and regenerate (`go install ./cmd/protoc-gen-mcp && make gen`).
5. Add a table-driven test + a golden fixture (generator changes) or an e2e test under `examples/<svc>/` (runtime changes).
6. Update `README.md` annotation reference if the surface changed.
7. Run the full conformance checklist above.

## Questions to always answer in the PR description

- What changed in the user-visible surface?
- What would a user do differently now?
- What's the migration path if this is not fully backward-compatible?
- If this adds a knob, what's the default and why?
