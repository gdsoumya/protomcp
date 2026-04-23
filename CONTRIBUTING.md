# Contributing to protomcp

Thanks for considering a contribution. This document covers how to set up a dev environment, what we expect from a change, and the design principles we hold the library to.

## Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Go | ≥ 1.26 | build & test |
| [`buf`](https://buf.build/docs/installation) | ≥ 1.50 | proto lint + codegen |
| [`protoc-gen-go`](https://pkg.go.dev/google.golang.org/protobuf/cmd/protoc-gen-go) | latest | proto → Go |
| [`protoc-gen-go-grpc`](https://pkg.go.dev/google.golang.org/grpc/cmd/protoc-gen-go-grpc) | latest | proto → gRPC stubs |
| [`golangci-lint`](https://golangci-lint.run/usage/install/) | ≥ 2.11 | lint (v2 config format) |

Install the Go tooling:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/bufbuild/buf/cmd/buf@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# protomcp's own plugin, rebuild after every change to cmd/protoc-gen-mcp or internal/gen
go install ./cmd/protoc-gen-mcp
```

## Workflow

```bash
make gen     # regenerate .pb.go, *_grpc.pb.go, *.mcp.pb.go from proto/
make test    # go test -race -count=1 ./...
make lint    # golangci-lint + buf lint
```

`make gen` expects the tools above to be on `$PATH`. If you modify `cmd/protoc-gen-mcp/` or anything under `internal/gen/`, rebuild the plugin first (`go install ./cmd/protoc-gen-mcp`) and regenerate.

## Repository buf layout

`buf.yaml` declares **two modules** in one workspace:

| Module | Path | Published? | Purpose |
|---|---|---|---|
| Annotations | `proto/` (excludes `proto/examples`) | ✅ as `buf.build/gdsoumya/protomcp` | the user-facing annotation schema |
| Examples | `proto/examples/` | ❌ no `name` | local-only demo services |

Both are linted and both are picked up by `buf generate`. Only the first is pushed to BSR.

### Publishing an annotation-schema change

Publishing is automated by `.github/workflows/buf-push.yml`:

- Every push to master updates the BSR `main` label, so consumers pinning
  `buf.build/gdsoumya/protomcp` (no label) track master.
- Every published GitHub release applies the tag name as a label, so
  consumers can pin `buf.build/gdsoumya/protomcp:vX.Y.Z`.

Both flows use `buf push --exclude-unnamed` so the unnamed
`proto/examples` workspace module stays local.

For a one-off manual push (correcting a bad release, pushing from a
fork, etc.):

```bash
buf lint
buf build
# Update both main and the tag in one push.
buf push --label main --label vX.Y.Z --exclude-unnamed
```

`buf push` without `--exclude-unnamed` fails with *"a name must be
specified in buf.yaml to push module: path: proto/examples"*; the flag
is how we tell buf to publish the named module and skip the other.

Annotation schema changes are subject to stricter rules:

- **No tag-number reuse, no removed fields, no renames.** `protomcp.v1`
  is consumed by downstream generators; breaking it breaks everyone's
  generated code.
- **`buf breaking`** runs against the BSR copy in CI; if it flags a
  real break, push the change as a new `v2` package instead.
- **Tag releases use labels, not commits.** Label immutability is
  configured on the BSR module so `vX.Y.Z` cannot be moved once pushed.

## Conformance requirements for a PR

A PR will not be merged unless every item below is true:

- [ ] `make test` is clean (`-race -count=1` passes on every package)
- [ ] `make lint` is clean (`golangci-lint run` returns zero findings)
- [ ] Generated `.pb.go` / `*_grpc.pb.go` / `*.mcp.pb.go` files are checked in and current (`make gen` leaves no diff)
- [ ] Every new proto message / RPC has a leading comment (the generator surfaces them as JSON Schema `description`s to the LLM client; missing descriptions degrade the tool's usability)
- [ ] Annotation schema changes (`proto/protomcp/v1/annotations.proto`) preserve backward compatibility, new fields only, no renumbering, no removed fields
- [ ] SDK behaviour that differs from upstream is documented with a `// Why:` comment citing the incident or constraint
- [ ] Public-facing changes to `pkg/protomcp` have table-driven tests; generator changes have golden fixtures under `internal/gen/testdata/`
- [ ] The generator never prints to stdout outside a `protogen.Plugin` response (stdout is the protoc wire channel, any stray write corrupts codegen for every downstream user)

## Design principles

These are load-bearing, if a PR violates one, call it out explicitly in the description and explain why.

### 1. Annotation-required rendering

An RPC is exposed as an MCP primitive **only** when it carries one of `protomcp.v1.tool`, `resource_template`, `resource_list`, `resource_list_changed`, or `prompt` (elicitation is a modifier and requires a sibling `tool`). Unannotated RPCs never leak. There is no denylist mode and no auto-expose default. The rationale is safety: `protoc-gen-mcp` runs in repos that contain internal-only RPCs we must not accidentally expose to an LLM.

### 2. Thin wrapper over the SDK

Everything MCP-protocol, JSON-RPC framing, capability negotiation, session, progress SSE, cancellation, is delegated to `github.com/modelcontextprotocol/go-sdk`. We never hand-roll protocol logic. If a feature needs protocol changes, it belongs upstream in the SDK, not here.

### 3. No hidden state

Generator output is deterministic: the same `.proto` + the same plugin version always produces byte-identical `*.mcp.pb.go`. Tests that can't reproduce the generator's output (non-deterministic iteration, timestamps, random IDs) are wrong. `json.Marshal` is the blessed serializer because it sorts object keys.

### 4. Generic library

protomcp is a general-purpose OSS library. No product-specific, no single-vendor integrations live in this repo. Examples are minimal and runnable standalone.

### 5. `google.api.field_behavior` is the OUTPUT_ONLY contract

`OUTPUT_ONLY` fields are stripped from the input schema **and** zeroed at runtime before the upstream gRPC call. The schema is advisory, the runtime clear is the actual security guarantee. Never remove `protomcp.ClearOutputOnly` from the generated handler; never relax it to top-level only. Nested / repeated / map-valued messages must recurse (see `pkg/protomcp/fieldbehavior_test.go`).

### 6. Streaming rules are primitive-specific, not warnings

Client-streaming and bidi RPCs are hard generation errors for every annotation; there is no sensible protojson ↔ MCP mapping for duplex streams. Per-primitive rules:

- `tool` accepts unary or server-streaming (server-streaming maps to MCP progress notifications).
- `resource_template`, `resource_list`, `prompt`, `elicitation` are unary-only.
- `resource_list_changed` **requires** server-streaming; the annotated RPC is the change-feed source.

Any deviation is a hard error citing the `service.method`. No fallbacks.

### 7. Schema mismatches are dev-time failures

`AddTool` is called with both input and output schemas. The SDK validates responses against the output schema before delivering them, a proto server returning a response that fails our generated schema is a codegen bug we want the test suite to catch, not something to silence.

## Style

- Go: follow `gofmt` + `goimports`; the lint config is authoritative.
- Proto: follow `buf` standard rules (currently configured in `buf.yaml`).
- Generator output: imports use `protogen.GoImportPath` aliasing; template text is free of magic package names (they come from `QualifiedGoIdent`). No raw import strings in templates.
- Comments: lead with the reason (the *why*), not the restatement. See `pkg/protomcp/server.go` for the house style.

## Adding a new annotation field

1. Add the field to `proto/protomcp/v1/annotations.proto` with a new tag number (never reuse).
2. `make gen` to regenerate `pkg/api/gen/protomcp/v1/annotations.pb.go`.
3. Thread the field through `internal/gen/annotations.go` and the relevant per-primitive builder (`generator.go`, `resources.go`, or `prompt.go`) into the matching template under `internal/gen/templates/`.
4. Update the annotation reference table in `README.md`.
5. Add a fixture in `internal/gen/testdata/` and a test in `internal/gen/generator_test.go` exercising the new field.

## Adding an example

Examples must:

- run standalone (`go run ./examples/<name>/cmd/<binary>`)
- have a README explaining what it demonstrates
- have an e2e test that drives the real MCP client against the real server
- use only the public surface of `pkg/protomcp`

## Commit messages

Conventional Commits style is preferred but not enforced. The body should explain *why*, not restate the diff. Reference an issue where one exists.

## Reporting a security issue

See [SECURITY.md](./SECURITY.md). Use GitHub's [private vulnerability reporting](https://github.com/gdsoumya/protomcp/security/advisories/new); do not open a public issue.
