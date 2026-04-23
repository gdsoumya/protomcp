# protomcp annotations

[![Apache 2.0](https://img.shields.io/badge/license-Apache_2.0-blue)](https://github.com/gdsoumya/protomcp/blob/main/LICENSE)
[![GitHub](https://img.shields.io/badge/source-gdsoumya%2Fprotomcp-181717?logo=github)](https://github.com/gdsoumya/protomcp)
[![Go Reference](https://pkg.go.dev/badge/github.com/gdsoumya/protomcp.svg)](https://pkg.go.dev/github.com/gdsoumya/protomcp)

Protobuf annotations used by [**protomcp**](https://github.com/gdsoumya/protomcp), a Go library that turns any gRPC service into a [Model Context Protocol](https://modelcontextprotocol.io) server.

Add a method option to mark an RPC as an MCP primitive (tool, resource
template, resource list, resource list-changed feed, or prompt;
elicitation is a modifier on `tool`):

```proto
import "protomcp/v1/annotations.proto";

service Greeter {
  rpc SayHello(HelloRequest) returns (HelloReply) {
    option (protomcp.v1.tool) = {
      title:       "Say Hello"
      description: "Greets a caller by name."
      read_only:   true
    };
  }
}
```

Unannotated RPCs are **not exposed**; presence of the annotation is
the opt-in, by design.

## Using this module

```yaml
# buf.yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/gdsoumya/protomcp
```

Then run `buf generate` against your proto tree.

For the runtime library, generator plugin, quickstart, examples, authentication, and the full annotation reference, see the **[main README](https://github.com/gdsoumya/protomcp#readme)** on GitHub.

## What's in this module

| File | Purpose |
|---|---|
| [`protomcp/v1/annotations.proto`](./protomcp/v1/annotations.proto) | `ToolOptions`, `ResourceTemplateOptions`, `ResourceListOptions`, `ResourceListChangedOptions`, `PromptOptions`, `ElicitationOptions` (method-level), `ServiceOptions` (service-level), `PlaceholderBinding` |

## License

Apache 2.0, see [LICENSE](https://github.com/gdsoumya/protomcp/blob/main/LICENSE).
