<div align="center">

<img src="assets/protomcp.png" alt="protomcp" width="640">

<!--
The H1 heading is kept in source for tools that extract the first heading
(pkg.go.dev metadata, readme parsers, grep) but wrapped in an HTML
comment so GitHub does not render it twice — the logo image above is the
visual title, and its `alt="protomcp"` provides the accessible name.

# protomcp
-->

<br>

_Turn any gRPC service into an [MCP](https://modelcontextprotocol.io) server with a single proto annotation. Zero changes to your gRPC server._

[![CI](https://github.com/gdsoumya/protomcp/actions/workflows/ci.yml/badge.svg)](https://github.com/gdsoumya/protomcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/gdsoumya/protomcp.svg)](https://pkg.go.dev/github.com/gdsoumya/protomcp)
[![Apache 2.0](https://img.shields.io/badge/license-Apache_2.0-blue)](LICENSE)

</div>

```proto
service Greeter {
  rpc SayHello(HelloRequest) returns (HelloReply) {
+   option (protomcp.v1.tool) = {                       // ← annotate
+     title: "Say Hello"                                //   and you're
+     description: "Greets a caller by name."           //   done
+     read_only: true
+   };
  }
}
```

That's it. `protoc-gen-mcp` reads the annotation, emits an MCP tool handler bound to your existing gRPC client, and the official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) handles the protocol.

---

## Table of contents

- [Why protomcp](#why-protomcp)
- [Install](#install)
- [Quickstart](#quickstart)
- [Annotation reference](#annotation-reference)
- [Examples](#examples)
- [Authentication](#authentication)
- [Middleware, error handling, post-processing](#middleware-error-handling-post-processing)
- [Scope & limitations](#scope--limitations)
- [Repository layout](#repository-layout)
- [Development](#development)
- [Comparison with other projects](#comparison-with-other-projects)
- [License](#license)

---

## Why protomcp

- **Protoc plugin.** `protoc-gen-mcp` drops in next to `protoc-gen-go` and `protoc-gen-go-grpc`. Works with `buf generate` and vanilla `protoc`.
- **Thin runtime.** `pkg/protomcp` is a small layer over the official SDK — composable stdlib-style middleware that can write outgoing gRPC metadata (serving the same purpose as `grpc-gateway`'s `runtime.WithMetadata` annotator, but shaped as a chainable wrapper with access to the parsed request and a shared `metadata.MD`), pluggable error handling, response post-processors.
- **`*protomcp.Server` is an `http.Handler`.** Drops into stdlib, chi, gin, echo, fiber the same way grpc-gateway's `runtime.ServeMux` does.
- **Default-deny rendering.** An RPC is exposed only when it carries `option (protomcp.v1.tool)`. Unannotated stays private.
- **Uses the official MCP SDK.** We do not hand-roll JSON-RPC, sessions, progress SSE, or capability negotiation — the upstream SDK owns all protocol work.

---

## Install

### Protoc plugin

```bash
go install github.com/gdsoumya/protomcp/cmd/protoc-gen-mcp@latest
```

### Runtime library

```bash
go get github.com/gdsoumya/protomcp/pkg/protomcp@latest
```

### Annotation schema

Your `.proto` files `import "protomcp/v1/annotations.proto";`. For protoc / buf to resolve that import, the file itself has to live somewhere on the include path. There are three supported ways to make that happen — pick whichever matches how you already manage proto dependencies.

#### Option 1 — Buf Schema Registry (recommended for buf users)

The annotations are published on [BSR](https://buf.build) at [`buf.build/gdsoumya/protomcp`](https://buf.build/gdsoumya/protomcp). Add one line to your `buf.yaml` and `buf` fetches them on `buf generate`:

```yaml
# buf.yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/gdsoumya/protomcp
```

Versioned, cached, no files in your tree. Pin a specific commit with `buf.build/gdsoumya/protomcp:<commit>` if you need reproducible builds beyond what `buf.lock` gives you.

#### Option 2 — Vendor the file (works everywhere)

Copy `proto/protomcp/v1/annotations.proto` from this repo into your own proto tree, preserving the directory structure:

```
your-repo/
└── proto/
    ├── protomcp/v1/
    │   └── annotations.proto   # copied, verbatim
    └── myservice/v1/
        └── myservice.proto     # imports "protomcp/v1/annotations.proto"
```

Because the file is small (a single `extend MethodOptions { ... }` + a couple of messages) and its proto tag numbers are stable, a vendored copy is fine to keep for the life of your project. Update when protomcp releases bump the schema.

#### Option 3 — git submodule (shared proto tree)

If you already vendor proto via submodules, add this repo as one:

```bash
git submodule add https://github.com/gdsoumya/protomcp third_party/protomcp
```

Then point your include path at `third_party/protomcp/proto`:

- **buf** — list it as an additional module in `buf.yaml`:
  ```yaml
  version: v2
  modules:
    - path: proto
    - path: third_party/protomcp/proto
  ```
- **vanilla protoc** — add `-I third_party/protomcp/proto` to your `protoc` invocation (see [§ Run the plugin](#2-run-the-plugin)).

Regardless of which option you pick, the generated Go code still imports `pkg/protomcp` at runtime — that part is the [runtime library `go get`](#runtime-library) above. The three options here are purely about where the `.proto` file itself lives for the compiler.

---

## Quickstart

### 1. Annotate the RPCs you want exposed

```proto
syntax = "proto3";
package greeter.v1;

import "protomcp/v1/annotations.proto";
import "google/api/field_behavior.proto";

service Greeter {
  rpc SayHello(HelloRequest) returns (HelloReply) {
    option (protomcp.v1.tool) = {
      title: "Say Hello"
      description: "Greets a caller by name."
      read_only: true
    };
  }

  // Internal is intentionally unannotated — protomcp will not expose it.
  rpc Internal(HelloRequest) returns (HelloReply);
}

message HelloRequest {
  string name = 1 [(google.api.field_behavior) = REQUIRED];
}
message HelloReply {
  string message = 1;
}
```

### 2. Run the plugin

**With `buf`** (`buf.gen.yaml`):

```yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: gen/go
    opt: [paths=source_relative]
  - local: protoc-gen-go-grpc
    out: gen/go
    opt: [paths=source_relative]
  - local: protoc-gen-mcp
    out: gen/go
    opt: [paths=source_relative]
```

```bash
buf generate
```

**With vanilla `protoc`:**

```bash
protoc \
  --go_out=gen/go --go_opt=paths=source_relative \
  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
  --mcp_out=gen/go  --mcp_opt=paths=source_relative \
  -I proto -I third_party \
  proto/greeter/v1/greeter.proto
```

Either path produces `greeter.mcp.pb.go` alongside `greeter.pb.go` and `greeter_grpc.pb.go`.

### 3. Wire the generated registrar into an HTTP server

```go
conn, _ := grpc.NewClient("127.0.0.1:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
client := greeterv1.NewGreeterClient(conn)

srv := protomcp.New("greeter-mcp", "0.1.0")
greeterv1.RegisterGreeterMCPTools(srv, client)

// *protomcp.Server implements http.Handler — compose with any stack.
http.ListenAndServe(":8080", srv)

// ...or serve over stdio:
// srv.ServeStdio(ctx)
```

Full runnable version: [`examples/greeter/cmd/greeter/main.go`](examples/greeter/cmd/greeter/main.go).

---

## Annotation reference

> **Every annotation in protomcp is optional.** There is no annotation you are *forced* to write. The only mechanism that's *required-for-a-particular-behaviour* is the presence of `option (protomcp.v1.tool)` on an RPC — and even that is only required if you want that RPC exposed as an MCP tool. An RPC without the option is silently skipped; an unannotated proto file is a valid protomcp input that just produces zero tools.
>
> The bare minimum to expose an RPC is an empty option body:
>
> ```proto
> rpc SayHello(HelloRequest) returns (HelloReply) {
>   option (protomcp.v1.tool) = {};   // valid; uses every default
> }
> ```
>
> Everything below is about what you can layer on top of that.

### `protomcp.v1.tool` — the opt-in method option

| Field | Required? | Type | Default | Effect |
|---|---|---|---|---|
| *(presence of the option itself)* | **required to expose** | — | absent → RPC is skipped | An RPC becomes an MCP tool only when this option is present on it. |
| `name` | optional | string | `<Service>_<Method>` | Override the generated tool name. Validated at codegen against MCP's `[a-zA-Z0-9_.-]{≤128}` rule. |
| `title` | optional | string | — | Human-readable title shown to MCP clients. |
| `description` | optional | string | *leading proto comment* | Tool description. Falls back to the RPC's leading `//` comment when absent. |
| `read_only` | optional | bool | false | Emits `annotations.readOnlyHint: true`. Hints the LLM the tool is safe to call speculatively. |
| `idempotent` | optional | bool | false | Emits `annotations.idempotentHint: true`. Client knows retry is safe. |
| `destructive` | optional | bool | false | Emits `annotations.destructiveHint: true`. Client may confirm before calling. |
| `stream_mode` | optional | enum | `STREAM_MODE_PROGRESS` | Server-streaming behaviour. Currently only `PROGRESS` is supported; field is reserved for future shapes. |

### `protomcp.v1.service` — optional service-level options

| Field | Required? | Type | Default | Effect |
|---|---|---|---|---|
| `tool_prefix` | optional | string | — | Prepended verbatim to every tool name on this service. Include your own separator (`foo_`, `foo.`, `foo-`). |

### Reused annotations (we read, don't define) — all optional

| Annotation | Required? | Where applied | Effect |
|---|---|---|---|
| `google.api.field_behavior = REQUIRED` | optional | request field | Added to parent's JSON Schema `required[]`. |
| `google.api.field_behavior = OUTPUT_ONLY` | optional | any field | **Stripped** from input schemas and zeroed at runtime via `protomcp.ClearOutputOnly` (recursive: nested, repeated, map). Retained in output schemas. |
| `[deprecated = true]` | optional | any field | Emits JSON Schema `deprecated: true`. |
| Leading `//` comments | optional (recommended) | any field / message / RPC | Used as JSON Schema `description`. Comments are how you teach the LLM what your tool does — the most impactful "annotation" even though it's not technically one. |
| `buf.validate.field.*` | optional | request field | Translated to JSON Schema: `pattern`, `format` (uuid / email / ipv4 / ipv6 / hostname / uri + extended: tuuid / address / uri-reference / ip-with-prefixlen / ip-prefix / host-and-port), `minLength` / `maxLength`, numeric bounds (`minimum` / `maximum` / exclusive variants), `const`, `enum` (string in-list), `not: {enum}` (not-in), repeated `minItems` / `maxItems` / `uniqueItems`, map key/value constraints, enum `const` / `in` / `not_in` / `defined_only`, bytes length. |

---

## Examples

Each example is standalone, runnable, and has its own README.

| Example | Shows |
|---|---|
| [`examples/greeter`](examples/greeter) | Unary + server-streaming RPC, progress notifications, error handling, middleware mutation, response redaction, SDK options pass-through |
| [`examples/tasks`](examples/tasks) | Full CRUD with `read_only` / `idempotent` / `destructive` hints and `OUTPUT_ONLY` stripping |
| [`examples/auth`](examples/auth) | Two-layer auth: SDK-native bearer middleware **or** custom HTTP middleware, both writing gRPC metadata for the upstream |

Cmd directories inside each example hold the runnable binaries:

- [`examples/greeter/cmd/greeter`](examples/greeter/cmd/greeter) — baseline
- [`examples/greeter/cmd/streaming`](examples/greeter/cmd/streaming) — server-streaming demo
- [`examples/greeter/cmd/mutator`](examples/greeter/cmd/mutator) — middleware rewrites `GRPCRequest.Input`
- [`examples/greeter/cmd/redactor`](examples/greeter/cmd/redactor) — `ResultProcessor` scrubs PII
- [`examples/greeter/cmd/errorhandler`](examples/greeter/cmd/errorhandler) — custom `ErrorHandler`
- [`examples/greeter/cmd/sdkopts`](examples/greeter/cmd/sdkopts) — pass `mcp.ServerOptions` / `mcp.StreamableHTTPOptions`
- [`examples/tasks/cmd/tasks`](examples/tasks/cmd/tasks) — CRUD
- [`examples/auth/cmd/auth`](examples/auth/cmd/auth) — custom HTTP middleware → ctx → metadata
- [`examples/auth/cmd/sdkauth`](examples/auth/cmd/sdkauth) — SDK's `auth.RequireBearerToken` → `TokenInfoFromContext` → metadata

---

## Authentication

protomcp ships no opinionated auth layer. Since `*protomcp.Server` is an `http.Handler`, pick whichever of the following fits your deployment — or combine them:

### 1. SDK's native bearer-token middleware (recommended for OAuth 2.1)

`auth.RequireBearerToken` from the SDK is a `func(http.Handler) http.Handler` that parses `Authorization`, calls your `TokenVerifier`, stashes a `*auth.TokenInfo` on ctx, and handles `WWW-Authenticate` + RFC 9728 Protected Resource Metadata on 401.

```go
bearer := auth.RequireBearerToken(myVerifier, &auth.RequireBearerTokenOptions{
    ResourceMetadataURL: "https://api.example.com/.well-known/oauth-protected-resource",
    Scopes:              []string{"tools:call"},
})
http.ListenAndServe(":8080", bearer(srv))
```

### 2. Custom stdlib HTTP middleware

Already have an auth stack (session cookies, mTLS, API keys, SSO)? Wrap `srv` in an ordinary `func(http.Handler) http.Handler`. Reject unauthenticated requests with HTTP 401 and stash the resolved principal on `r.Context()`. Nothing protomcp-specific.

### 3. `protomcp.Middleware` bridges ctx → gRPC metadata

Whichever layer authenticated the caller, the final step is the same: read identity off ctx and write gRPC metadata so the upstream gRPC server can run its own authorization unchanged.

```go
func tokenInfoToMetadata() protomcp.Middleware {
    return func(next protomcp.Handler) protomcp.Handler {
        return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
            if info := auth.TokenInfoFromContext(ctx); info != nil {
                g.Metadata.Set("x-user-id", info.UserID)
                for _, scope := range info.Scopes {
                    g.Metadata.Append("x-scopes", scope)
                }
            }
            return next(ctx, req, g)
        }
    }
}

srv := protomcp.New("svc", "0.1.0", protomcp.WithMiddleware(tokenInfoToMetadata()))
```

The upstream gRPC server reads those keys via `metadata.FromIncomingContext` in its existing `UnaryServerInterceptor`. **Zero code changes on the gRPC side.**

### Confused-deputy warning

Forwarding a raw bearer token to the upstream gRPC server is safe **only when the upstream validates the token's `aud` claim against itself and enforces `scope`**. Otherwise any valid-looking token reaches any backend protomcp fronts. Alternatives:

- **Token exchange (RFC 8693)** — mint a new audience-bound token per backend.
- **Attested claims over a trusted channel** — forward only the resolved identity and trust it via mTLS or signed headers between protomcp and the backend.

---

## Middleware, error handling, post-processing

### Middleware — modify the outgoing gRPC call

```go
func injectTenant(next protomcp.Handler) protomcp.Handler {
    return func(ctx context.Context, req *mcp.CallToolRequest, g *protomcp.GRPCRequest) (*mcp.CallToolResult, error) {
        if r, ok := g.Input.(*foov1.CreateRequest); ok {
            r.TenantId = tenantFromCtx(ctx)
        }
        return next(ctx, req, g)
    }
}
```

`GRPCRequest.Input` is the typed proto the handler is about to send — mutate fields or replace the pointer. `GRPCRequest.Metadata` is the outgoing `metadata.MD`.

### ErrorHandler — customize how gRPC errors reach the LLM

| Error shape | Default MCP surface |
|---|---|
| gRPC `Unauthenticated` / `PermissionDenied` / `Canceled` / `DeadlineExceeded` | JSON-RPC error (`*jsonrpc.Error`) |
| Other gRPC status codes | `CallToolResult{IsError: true}` + structured `google.rpc.Status` |
| Plain Go errors | `CallToolResult{IsError: true}` with the error message |

Override with `protomcp.WithErrorHandler(...)` — mirrors `grpc-gateway`'s `runtime.WithErrorHandler` but produces JSON-RPC shapes.

### ResultProcessor — mutate responses before the client sees them

```go
func scrubEmails(_ context.Context, _ *mcp.CallToolRequest, r *mcp.CallToolResult) (*mcp.CallToolResult, error) {
    for _, c := range r.Content {
        if tc, ok := c.(*mcp.TextContent); ok {
            tc.Text = emailRE.ReplaceAllString(tc.Text, "[email]")
        }
    }
    return r, nil
}

protomcp.New("svc", "0.1.0", protomcp.WithResultProcessor(scrubEmails))
```

Processors run on **both** success and `IsError` results, so a single redaction rule covers every response path.

### SDK pass-through

```go
srv := protomcp.New("svc", "0.1.0",
    protomcp.WithSDKOptions(&mcp.ServerOptions{
        Logger:       slog.Default(),
        Instructions: "tools/ are read-only; caller is authenticated via bearer",
        KeepAlive:    30 * time.Second,
    }),
    protomcp.WithHTTPOptions(&mcp.StreamableHTTPOptions{
        JSONResponse: true,
    }),
)
```

---

## Scope & limitations

### Supported

- **proto3.** proto2 is not supported.
- **Unary RPCs** and **server-streaming RPCs**. Each streamed message is delivered as an MCP `notifications/progress` event (monotonic `progress` counter per spec, gRPC message serialized as protojson in the `message` field); the final `CallToolResult` carries a summary.
- JSON Schema built from the proto descriptor following protojson conventions: int64/uint64 as string, enums as string names, wrapper types as nullable primitives.
- Every `google.protobuf.*` well-known type (Timestamp, Duration, Any, Empty, Struct, Value, ListValue, FieldMask, all 9 wrappers), all scalar kinds, enums, maps, repeated, nested messages (with a cycle-breaking recursion cap).
- Proto3 `optional` (synthetic oneofs) as regular optional fields; real oneofs as JSON Schema `anyOf`.
- `buf.validate` / protovalidate constraints → JSON Schema keywords (see [annotation reference](#annotation-reference)).
- `google.api.field_behavior` — `REQUIRED` and `OUTPUT_ONLY` (recursive stripping at runtime).
- Field-level `description` from leading proto comments; `[deprecated = true]` → JSON Schema `deprecated`.
- Cross-file tool-name collision detection at codegen time.

### Not supported (by design)

- **Client-streaming** and **bidi-streaming** RPCs — no natural MCP mapping. Annotating one is a **hard generation error**, not a warning.
- proto2.
- Bundled auth providers. The middleware seam is the extension point.

### Server-streaming ↔ MCP progress

MCP's `notifications/progress` is intended for human-readable status updates; there is no first-class streaming-output shape for `tools/call`. protomcp (like every other proto→MCP generator we surveyed) delivers each gRPC streamed message as a progress notification with the message protojson-encoded in `message` and a monotonically increasing `progress` counter. If MCP adds a proper streaming shape, we'll migrate.

---

## Repository layout

```
proto/protomcp/v1/annotations.proto   # the annotation schema (ToolOptions, ServiceOptions)
cmd/protoc-gen-mcp/                   # plugin binary
internal/gen/                         # generator core
internal/gen/schema/                  # proto → JSON Schema
pkg/protomcp/                         # runtime library
pkg/api/gen/                          # generated .pb.go / *_grpc.pb.go / *.mcp.pb.go
examples/greeter/                     # unary + server-streaming demos
examples/tasks/                       # CRUD with all tool hints
examples/auth/                        # two-layer auth
```

---

## Development

```bash
make gen      # regenerate .pb.go / *_grpc.pb.go / *.mcp.pb.go from proto/
make test     # go test -race -count=1 ./...
make lint     # golangci-lint run
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor guide and [AGENTS.md](AGENTS.md) for guidance aimed at AI coding agents.

---

## Comparison with other projects

We surveyed every Go-based proto → MCP project we could find before starting. None met the requirements in [§ Why protomcp](#why-protomcp), so protomcp is a greenfield implementation. Here is how the active projects compare today (April 2026):

| | **protomcp** | [redpanda-data/protoc-gen-go-mcp](https://github.com/redpanda-data/protoc-gen-go-mcp) | [adiom-data/grpcmcp](https://github.com/adiom-data/grpcmcp) | [linkbreakers-com/grpc-mcp-gateway](https://github.com/linkbreakers-com/grpc-mcp-gateway) |
|---|---|---|---|---|
| **MCP protocol layer** | official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) | `mark3labs/mcp-go` or `modelcontextprotocol/go-sdk` (pluggable; user-selected at wire-up, no default) | `mark3labs/mcp-go` | hand-rolled ~318 LOC JSON-RPC mux |
| **Rendering policy** | default-deny (annotation-required) | default-expose (every RPC) | default-expose | default-deny |
| **Annotation schema** | `protomcp.v1.tool` + `service` + `field_behavior` + `buf.validate` | `buf.validate` + `google.api.field_behavior` (`REQUIRED` only) | `buf.validate` only (plus source-comment `jsonschema:ignore` / `jsonschema:hide`) | custom `mcp.gateway.v1` schema |
| **Tool hints** (`read_only` / `idempotent` / `destructive`) | ✅ | ❌ | ❌ | ✅ |
| **Middleware chain** (ctx → gRPC metadata) | ✅ composable `protomcp.Middleware` | ❌ | ❌ (static `--header` flag only) | ❌ |
| **Custom `ErrorHandler`** | ✅ gRPC-aware defaults | ❌ | ❌ | ❌ |
| **Response post-processing** | ✅ `ResultProcessor` | ❌ (`fix.go` is input-side OpenAI cleanup, not a user hook) | ❌ | ❌ |
| **`OUTPUT_ONLY` stripping** | input schema **and** recursive runtime clear (nested / repeated / map) | ❌ (no `OUTPUT_ONLY` handling) | ❌ (no `field_behavior` support) | input schema only |
| **`http.Handler` directly** | ✅ `*protomcp.Server` is `http.Handler` | needs wiring | needs wiring (`server.NewSSEServer(...).Start()`) | ✅ `MCPServeMux` is `http.Handler` |
| **Transports** | stdio + streamable-HTTP | stdio + HTTP | stdio + HTTP (SSE) | HTTP only |
| **Unary RPCs** | ✅ | ✅ | ✅ | ✅ |
| **Server-streaming** | ✅ MCP progress notifications + monotonic counter | ❌ | ❌ | ❌ |
| **Client-streaming / bidi** | generation error (by design) | skipped silently | skipped silently | skipped silently |
| **Cross-file tool-name collision detection** | ✅ hard error at codegen | ❌ (silent SDK override at runtime) | ❌ | ❌ (silent mux override) |
| **`buf.validate` rule coverage** | strings / bytes / all numerics / enums / bool / repeated / maps / extended formats (uri-reference, tuuid, ip-with-prefixlen, ip-prefix, host-and-port, address, …) | strings (pattern / length / uuid / email) / int32/64 + uint32/64 bounds / float + double bounds — no enums, no repeated, no bytes, no booleans, no extended formats | strings / bytes / numerics / booleans / enums / repeated / maps + extended string formats (uri_ref, tuuid, ip_prefix, host_and_port, …) | ❌ (no `buf.validate` support) |
| **`google.api.field_behavior`** | `REQUIRED` + `OUTPUT_ONLY` (recursive runtime clear) | `REQUIRED` only | ❌ (via `buf.validate.required` only) | `REQUIRED` + `OUTPUT_ONLY` (codegen only, no runtime clear) |
| **Status** | actively developed | active (~195⭐) | active (~40⭐) | dormant since Feb 2026 (~5⭐) |

### Where protomcp specifically differs

- **Official SDK, exclusively.** JSON-RPC framing, session management, progress SSE, cancellation, and capability negotiation are all handled by `modelcontextprotocol/go-sdk`. Bug fixes and protocol updates flow through upstream. Redpanda ships *adapters* for both SDKs (pluggable, no default) but neither is idiomatic to their codebase; adiom uses `mark3labs/mcp-go`; linkbreakers hand-rolls the protocol with no SDK at all.
- **Auth is a first-class extension seam, not an afterthought.** The `protomcp.Middleware` type composes like stdlib HTTP middleware but has access to both the parsed tool request **and** the outgoing gRPC metadata, so a single function can verify a caller AND propagate identity to the upstream gRPC server. None of the other three projects expose a middleware or per-request ctx hook; users have to build auth + metadata propagation on their own. Adiom exposes a `--header` CLI flag for *static* headers only.
- **`OUTPUT_ONLY` is enforced end-to-end.** Only linkbreakers strips it from the input schema at all (via raw protowire parsing of `google.api.field_behavior`); redpanda and adiom ignore it entirely — redpanda reads `field_behavior` but only for `REQUIRED`, adiom doesn't read it at all. Nobody else runs a runtime clear. protomcp does both: strips from the schema AND runs `ClearOutputOnly` (recursive — nested, repeated, map) on every tool call so a malicious or sloppy client cannot forge server-computed fields by bypassing the advertised schema.
- **Server-streaming is supported.** Each gRPC message becomes a `notifications/progress` event (monotonic counter per MCP spec) with a final summary `CallToolResult`. All three other projects skip streaming RPCs entirely.
- **Tool-name collisions fail at codegen, not silently at runtime.** Both the SDK's `AddTool` and linkbreakers' `MCPServeMux.RegisterTool` *replace* a duplicate name without warning; our generator refuses to emit a colliding pair and cites both declaration sites.
- **Pluggable `ErrorHandler` and `ResultProcessor`.** Customize how gRPC status codes map to MCP error shapes; redact or rewrite responses after the tool handler runs. No other project offers either hook.
- **Client-streaming / bidi annotated RPCs are hard errors.** The other three silently skip them; we surface the mistake at codegen time.

Acknowledgement: the idea of "annotation-required to expose" came from looking at `linkbreakers-com/grpc-mcp-gateway` before we started; every other design choice here is our own.

---

## License

[Apache 2.0](LICENSE).
