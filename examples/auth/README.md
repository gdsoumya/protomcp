# auth examples

These demos show the two halves of the protomcp auth story:

1. **HTTP transport layer**, authenticate the caller, reject bad
   credentials with HTTP 401, put the resolved principal on ctx.
2. **MCP-call layer**, a `protomcp.ToolMiddleware` reads the principal
   off ctx and copies it into outgoing gRPC metadata so the upstream
   gRPC server can run its existing authorization interceptors
   unchanged.

The gRPC side is shared between both demos (`server/server.go`): a
`Profile` service with `PrincipalInterceptor` that reads `x-user-id`
and `x-tenant` from incoming metadata and rejects calls missing
`x-user-id` with `codes.Unauthenticated`.

| Demo | Auth path |
|---|---|
| [`cmd/auth`](cmd/auth) | **Custom stdlib HTTP middleware**, a hand-rolled `func(http.Handler) http.Handler` validates a bearer token against a hardcoded map. Useful when you already have an auth stack (session cookies, mTLS, API keys, custom headers) and want to plug protomcp into it. |
| [`cmd/sdkauth`](cmd/sdkauth) | **MCP SDK's native `auth.RequireBearerToken`**, the SDK does the `Authorization` header parsing, scope enforcement, and RFC 6750 / 9728 `WWW-Authenticate` on 401. The right choice when you want MCP-spec-aligned OAuth 2.1 out of the box. |

Both demos end in the same place: a `protomcp.ToolMiddleware` writes
`x-user-id` and `x-tenant` into `g.Metadata`, and the gRPC server's
interceptor reads them via `metadata.FromIncomingContext`.

## Run

```bash
# Custom HTTP middleware path.
go run ./examples/auth/cmd/auth &

# SDK native bearer middleware path.
go run ./examples/auth/cmd/sdkauth &
```

Each listens on `:8080` by default (override with `-addr`).

## Exercise

Successful call (alice is in the demo's valid-token table):

```bash
curl -sN -X POST \
  -H 'Authorization: Bearer alice-token' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
       "params":{"protocolVersion":"2025-06-18","capabilities":{},
                 "clientInfo":{"name":"curl","version":"0"}}}' \
  http://localhost:8080/
```

Unauthenticated call (should return HTTP 401):

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  -X POST http://localhost:8080/
```

The `sdkauth` demo additionally sends a spec-compliant
`WWW-Authenticate` header on the 401, which the `auth` demo does
not, compare with:

```bash
curl -sI -X POST http://localhost:8080/
```

## Tests

Full acceptance tests live alongside the binaries:

```bash
go test -race -count=1 ./examples/auth/...
```

They cover valid-token → `x-user-id`/`x-tenant` propagated → gRPC
handler observes them, and invalid-token → HTTP 401 before the MCP
SDK sees the request. Both paths are verified over the real
streamable-HTTP transport (not just in-memory).
