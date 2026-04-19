// Command protoc-gen-mcp is a protoc plugin that generates Go code which
// registers annotated gRPC methods as MCP tools against a protomcp.Server.
//
// The plugin is driven by the standard google.golang.org/protobuf/compiler/
// protogen harness; it is invoked as part of `buf generate` or
// `protoc --mcp_out=...` and emits one <file>.mcp.pb.go per input .proto
// file that contains at least one annotated RPC.
package main

import (
	"flag"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/gdsoumya/protomcp/internal/gen"
)

func main() {
	var flags flag.FlagSet
	maxDepth := flags.Int("max_recursion_depth", 0,
		"maximum recursive-message expansion depth in generated JSON schemas "+
			"(0 uses the library default of 3)")

	protogen.Options{ParamFunc: flags.Set}.Run(func(p *protogen.Plugin) error {
		return gen.GenerateWithOptions(p, gen.Options{MaxRecursionDepth: *maxDepth})
	})
}
