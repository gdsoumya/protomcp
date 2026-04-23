# greeter examples

Each subdirectory under `cmd/` is a self-contained `main.go` that
wires up the `Greeter` gRPC service (defined in `proto/examples/greeter/v1/`
and implemented in `server/server.go`) and exposes it over MCP via
`protomcp`. Every demo reuses the same gRPC service; the variations
sit in how the MCP side is configured.

Run any demo with `go run ./examples/greeter/cmd/<name>`. Binaries
that host an HTTP server accept `-addr` (default `:8080`); the
streaming demo accepts `-name` and `-turns`.

| Demo | What it shows |
|---|---|
| [`cmd/greeter`](cmd/greeter) | The minimal end-to-end wiring, gRPC + MCP on a single HTTP port, no middleware, no extras. Start here. |
| [`cmd/streaming`](cmd/streaming) | Server-streaming RPCs → MCP `notifications/progress` events. Runs everything in-process (client + server) and prints each streamed message as it arrives. |
| [`cmd/mutator`](cmd/mutator) | A `protomcp.ToolMiddleware` rewriting `GRPCRequest.Input` via type assertion. Demonstrates that field mutations in middleware propagate to the upstream gRPC call. |
| [`cmd/redactor`](cmd/redactor) | A `protomcp.ToolResultProcessor` redacting email-shaped substrings from tool responses before they reach the client. |
| [`cmd/errorhandler`](cmd/errorhandler) | A custom `protomcp.ToolErrorHandler` rewriting `codes.NotFound` errors for LLM-friendliness while delegating everything else to `DefaultErrorHandler`. |
| [`cmd/sdkopts`](cmd/sdkopts) | `WithSDKOptions` (slog logger + Instructions) and `WithHTTPOptions` (`JSONResponse: true`) pass-through. Useful when you need fine-grained control over the SDK. |

## Exercising a demo from the command line

Most demos host a streamable-HTTP MCP endpoint on `:8080`. A typical
initialize round-trip:

```bash
go run ./examples/greeter/cmd/greeter &

curl -sN -X POST \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
       "params":{"protocolVersion":"2025-06-18","capabilities":{},
                 "clientInfo":{"name":"curl","version":"0"}}}' \
  http://localhost:8080/
```

Expect a single SSE `event: message` payload containing the server's
`InitializeResult`. Subsequent requests can issue `tools/list` and
`tools/call` the same way.

For the streaming demo, no HTTP is involved, it runs an in-memory
MCP client inside the binary and prints progress notifications to
stdout:

```bash
go run ./examples/greeter/cmd/streaming -name "Alice" -turns 5
```

## Tests

The `examples/greeter/*_test.go` files exercise the same paths via the
SDK's in-memory transports, they are the authoritative behavioural
specification for the runtime. Run with:

```bash
go test -race -count=1 ./examples/greeter/...
```
