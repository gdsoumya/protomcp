package gen

import (
	_ "embed"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	// Side-effect imports: register the annotation extension and the fixture
	// protos in the global protoregistry so protogen can resolve extension
	// types when walking the descriptors embedded in fixtures.binpb.
	_ "github.com/gdsoumya/protomcp/internal/gen/testdata/greeter"
	_ "github.com/gdsoumya/protomcp/internal/gen/testdata/multi"
	_ "github.com/gdsoumya/protomcp/internal/gen/testdata/notools"
	_ "github.com/gdsoumya/protomcp/internal/gen/testdata/options"
	_ "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// fixturesBin is the output of:
//
//	protoc --include_source_info --include_imports \
//	    --descriptor_set_out=fixtures.binpb \
//	    greeter.proto options_variety.proto multi_service.proto no_tools.proto
//
// It holds FileDescriptorProto messages for every testdata fixture, with
// source_code_info populated so our leading-comment-fallback assertions
// actually have comment text to observe (the runtime protoregistry does
// not carry source info).
//
//go:embed testdata/fixtures.binpb
var fixturesBin []byte

// TestGenerate_Greeter drives the real generator against the committed
// greeter.proto fixture and asserts the resulting *.mcp.pb.go contains
// every expected symbol (and none of the ones that should be skipped).
func TestGenerate_Greeter(t *testing.T) {
	out := runGenerate(t, "greeter.proto")

	cases := []substringCase{
		{"register function", true, "RegisterGreeterMCPTools"},
		{"SayHello tool", true, `"Greeter_SayHello"`},
		{"StreamGreetings tool", true, `"Greeter_StreamGreetings"`},
		{"unannotated Internal RPC is not exposed", false, "\"Greeter_Internal\""},
		{"unannotated Internal RPC does not appear at all", false, "Greeter_Internal_InputSchema"},
		{"unannotated BatchGreet (client-streaming) is not exposed", false, `"Greeter_BatchGreet"`},
		{"unannotated Chat (bidi) is not exposed", false, `"Greeter_Chat"`},
		// Skip comments were removed — unsupported streaming shapes now
		// either produce nothing (when unannotated) or a hard error (when
		// annotated; see TestGenerate_BadStreams_ClientErrors / _BidiErrors).
		{"no skip comments in output", false, "protoc-gen-mcp: skipping"},
		{"server-streaming emits progress loop", true, "NotifyProgress"},
		{"unary handler path", true, "client.SayHello(ctx, upstream)"},
		{"streaming handler path", true, "client.StreamGreetings(ctx, upstream)"},
		{"reads Input from GRPCRequest (type-assert)", true, "g.Input.(*"},
	}
	assertSubstrings(t, out, cases)
}

// TestGenerate_BadStreams_ClientErrors asserts the generator returns a
// clear error when a client-streaming RPC is annotated with protomcp.v1.tool.
func TestGenerate_BadStreams_ClientErrors(t *testing.T) {
	err := runGenerateExpectError(t, "bad_streams.proto")
	want := "BadClient.Push: client-streaming RPCs cannot be exposed as MCP tools"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v", want, err)
	}
}

// TestGenerate_BadStreams_BidiErrors asserts the generator returns a
// clear error when a bidi-streaming RPC is annotated with protomcp.v1.tool.
func TestGenerate_BadStreams_BidiErrors(t *testing.T) {
	err := runGenerateExpectError(t, "bad_bidi.proto")
	want := "BadBidi.Duplex: bidi-streaming RPCs cannot be exposed as MCP tools"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v", want, err)
	}
}

// TestGenerate_OptionsVariety covers service-level tool_prefix, explicit
// tool name override (with prefix), every combination of hint flags, the
// description-override vs. leading-comment fallback, and the server-
// streaming + explicit PROGRESS stream_mode branch.
func TestGenerate_OptionsVariety(t *testing.T) {
	out := runGenerate(t, "options_variety.proto")

	cases := []substringCase{
		// Service-level prefix is applied to the synthesized name.
		{"prefix + synthesized name", true, `"ns_Prefixed_ReadOnlyOnly"`},
		{"prefix + synthesized name (IdempotentOnly)", true, `"ns_Prefixed_IdempotentOnly"`},
		{"prefix + synthesized name (DestructiveOnly)", true, `"ns_Prefixed_DestructiveOnly"`},
		{"prefix + synthesized name (AllHints)", true, `"ns_Prefixed_AllHints"`},
		{"prefix + synthesized name (NoHints)", true, `"ns_Prefixed_NoHints"`},

		// Explicit name override is used verbatim on top of the prefix. Per
		// the generator, an explicit override is NOT sanitized — the user's
		// string is preserved so dots/slashes survive.
		{"prefix + override preserved verbatim", true, `"ns_custom.name.value"`},
		// The synthesized fallback "ns_Prefixed_Renamed" must NOT appear.
		{"synthesized name does not leak when override set", false, `"ns_Prefixed_Renamed"`},

		// Hint combinations. Each annotation literal must contain exactly
		// the flags that were set — and nothing else.
		{"ReadOnlyOnly has ReadOnlyHint",
			true, "&mcp.ToolAnnotations{ReadOnlyHint: true}"},
		{"IdempotentOnly has IdempotentHint",
			true, "&mcp.ToolAnnotations{IdempotentHint: true}"},
		{"DestructiveOnly has DestructiveHint",
			true, "&mcp.ToolAnnotations{DestructiveHint: protomcp.BoolPtr(true)}"},
		{"AllHints has all three fields",
			true, "&mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: protomcp.BoolPtr(true)}"},

		// Description override vs. leading-comment fallback.
		// The gofmt-aligned output uses two spaces after "Description:" when
		// it lines up with longer neighboring keys (e.g. "OutputSchema:"),
		// so we match on the quoted value alone.
		{"description override is used verbatim",
			true, `"explicit description wins"`},
		{"leading comment is used as fallback description",
			true, `"DescFallback has no description override. The generator must fall back\nto this leading proto comment."`},

		// Server-streaming with explicit PROGRESS mode still emits the
		// streaming template branch.
		{"ProgressStream emits NotifyProgress", true, "NotifyProgress"},
		{"ProgressStream is a tool", true, `"ns_Prefixed_ProgressStream"`},
	}
	assertSubstrings(t, out, cases)

	// NoHints has every hint flag clear. The ToolAnnotations struct literal
	// must NOT be emitted for it at all. We slice the output to the NoHints
	// tool block (the region between the "ns_Prefixed_NoHints" Name line
	// and the next AddTool call) and assert Annotations: never appears.
	assertNoAnnotationsInBlock(t, out, `"ns_Prefixed_NoHints"`)
}

// TestGenerate_MultiService asserts both services in a multi-service proto
// produce their own Register<X>MCPTools function, and that a bidi-streaming
// RPC is skipped with the bidi-specific comment (distinct from the
// client-streaming comment).
func TestGenerate_MultiService(t *testing.T) {
	out := runGenerate(t, "multi_service.proto")

	cases := []substringCase{
		{"Alpha register function", true, "func RegisterAlphaMCPTools("},
		{"Beta register function", true, "func RegisterBetaMCPTools("},
		{"Alpha tool", true, `"Alpha_Ping"`},
		{"Beta tool", true, `"Beta_Echo"`},
		{"unannotated bidi Duplex is not registered", false, `"Beta_Duplex"`},
		{"no skip comments", false, "protoc-gen-mcp: skipping"},
	}
	assertSubstrings(t, out, cases)
}

// TestGenerate_NoTools asserts that a proto with no annotated methods
// produces no generated file at all.
func TestGenerate_NoTools(t *testing.T) {
	req := buildGenRequest(t, "no_tools.proto")
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	if err := Generate(plugin); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	resp := plugin.Response()
	if resp.Error != nil {
		t.Fatalf("plugin error: %s", *resp.Error)
	}
	if got := len(resp.File); got != 0 {
		t.Fatalf("expected 0 generated files for a proto with no annotated "+
			"methods, got %d:\n%s", got, resp.File[0].GetContent())
	}
}

// TestSanitizeToolName covers the tool-name sanitizer directly. Proto
// service names are constrained to identifier characters, so dots and
// slashes can only leak into a synthesized tool name via a malformed or
// hand-crafted descriptor — but the sanitizer is defensive code the rest
// of the generator relies on, so we verify it behaves as advertised.
func TestSanitizeToolName(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"Greeter_SayHello", false},
		{"ns.v1.Greeter_SayHello", false}, // dots are legal per MCP spec
		{"Greeter-SayHello", false},       // dashes too
		{"a/b/c", true},                   // slash is not in [a-zA-Z0-9_.-]
		{"has a space", true},
		{"", true},
		{strings.Repeat("x", 129), true},
	}
	for _, tc := range cases {
		err := validateToolName(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateToolName(%q) = %v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

// --- test helpers ------------------------------------------------------

// substringCase is the shared shape for table-driven substring assertions
// on generator output: contains=true means "the output must contain needle",
// contains=false means "the output must NOT contain needle".
type substringCase struct {
	name     string
	contains bool
	needle   string
}

// runGenerate drives the generator against the named proto (which must be
// one of the files packed into fixtures.binpb) and returns the single
// generated file's content. It fails the test if anything other than one
// file is emitted.
func runGenerate(t *testing.T, protoName string) string {
	t.Helper()
	req := buildGenRequest(t, protoName)
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	if err := Generate(plugin); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	resp := plugin.Response()
	if resp.Error != nil {
		t.Fatalf("plugin error: %s", *resp.Error)
	}
	if got := len(resp.File); got != 1 {
		t.Fatalf("expected 1 generated file, got %d", got)
	}
	wantFilename := strings.TrimSuffix(protoName, ".proto") + ".mcp.pb.go"
	if !strings.HasSuffix(resp.File[0].GetName(), wantFilename) {
		t.Errorf("output filename = %q, want suffix %q",
			resp.File[0].GetName(), wantFilename)
	}
	return resp.File[0].GetContent()
}

// runGenerateExpectError drives the generator against protoName and
// returns the error it produced. It fails the test if the generator
// unexpectedly succeeded.
func runGenerateExpectError(t *testing.T, protoName string) error {
	t.Helper()
	req := buildGenRequest(t, protoName)
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	genErr := Generate(plugin)
	if genErr != nil {
		return genErr
	}
	// If Generate returned nil, the plugin may have surfaced the error via
	// its Response().Error field instead — check there too.
	if resp := plugin.Response(); resp.Error != nil {
		return fmt.Errorf("%s", *resp.Error)
	}
	t.Fatalf("expected generator error for %q, got success", protoName)
	return nil
}

// assertSubstrings runs a table of substring presence/absence checks
// against out, dumping the full file on failure so the failing assertion
// has enough context to debug.
func assertSubstrings(t *testing.T, out string, cases []substringCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Contains(out, tc.needle)
			if got != tc.contains {
				t.Errorf("contains(%q) = %v, want %v\n--- file ---\n%s",
					tc.needle, got, tc.contains, out)
			}
		})
	}
}

// assertNoAnnotationsInBlock slices out the registration block beginning
// at toolNameMarker and extending to the next AddTool call, then fails
// the test if "Annotations:" appears inside that window. It lets us
// assert the "no hints set -> no Annotations field" contract without
// being fooled by neighboring tools' literals.
func assertNoAnnotationsInBlock(t *testing.T, out, toolNameMarker string) {
	t.Helper()
	idx := strings.Index(out, toolNameMarker)
	if idx < 0 {
		t.Fatalf("marker %q not found in generated output:\n%s", toolNameMarker, out)
	}
	block := out[idx:]
	if next := strings.Index(block[1:], "AddTool("); next >= 0 {
		block = block[:next+1]
	}
	if strings.Contains(block, "Annotations:") {
		t.Errorf("tool block at %q must not emit an Annotations field, but got:\n%s",
			toolNameMarker, block)
	}
}

// buildGenRequest constructs a CodeGeneratorRequest from the precompiled
// fixtures.binpb FileDescriptorSet. The first argument names the file to
// generate; all transitively imported files are included as context so
// protogen can resolve cross-file references.
func buildGenRequest(t *testing.T, target string) *pluginpb.CodeGeneratorRequest {
	t.Helper()

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(fixturesBin, &fds); err != nil {
		t.Fatalf("unmarshal fixtures.binpb: %v", err)
	}

	// Sanity: target must exist in the set.
	found := false
	for _, f := range fds.File {
		if f.GetName() == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("target proto %q not present in fixtures.binpb; regenerate it with "+
			"protoc --include_source_info --include_imports", target)
	}

	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{target},
		// ProtoFile must include every file transitively referenced, in
		// dependency order. protoc's --include_imports already orders deps
		// before dependents, so we pass the set through unchanged.
		ProtoFile: fds.File,
		CompilerVersion: &pluginpb.Version{
			Major: proto.Int32(3),
			Minor: proto.Int32(21),
			Patch: proto.Int32(12),
		},
	}
}
