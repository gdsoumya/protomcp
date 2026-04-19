package gen

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// Import paths of every external package the generated file references.
// Centralizing them here (1) keeps the template text free of magic strings
// and (2) lets protogen's import bookkeeping resolve collisions.
const (
	importContext      = protogen.GoImportPath("context")
	importJSON         = protogen.GoImportPath("encoding/json")
	importErrors       = protogen.GoImportPath("errors")
	importFmt          = protogen.GoImportPath("fmt")
	importIO           = protogen.GoImportPath("io")
	importProtomcp     = protogen.GoImportPath("github.com/gdsoumya/protomcp/pkg/protomcp")
	importMCP          = protogen.GoImportPath("github.com/modelcontextprotocol/go-sdk/mcp")
	importGRPCMetadata = protogen.GoImportPath("google.golang.org/grpc/metadata")
	importProtojson    = protogen.GoImportPath("google.golang.org/protobuf/encoding/protojson")
)

// Options controls generator behavior that's tunable from plugin flags.
type Options struct {
	// MaxRecursionDepth caps how many times a recursive message type may
	// be expanded in a generated JSON Schema along a single path. Zero
	// uses the library default (schema.defaultMaxRecursionDepth, currently
	// 3). Exposed as -max_recursion_depth=N on protoc-gen-mcp.
	MaxRecursionDepth int
}

// Generate is the protogen entry point wired in from cmd/protoc-gen-mcp.
// It uses default Options; see GenerateWithOptions for flag-configurable
// runs.
func Generate(plugin *protogen.Plugin) error {
	return GenerateWithOptions(plugin, Options{})
}

// GenerateWithOptions is the configurable entry point. It walks every
// file the user asked to generate, skipping those without any annotated
// methods, and emits one <file>.mcp.pb.go per qualifying file.
func GenerateWithOptions(plugin *protogen.Plugin, opts Options) error {
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	// Tool names must be unique across the entire generation run, not just
	// per file — the SDK's mcp.AddTool silently *replaces* an existing tool
	// with the same name, so a cross-file collision would make one of the
	// tools disappear without warning at runtime. We thread a single shared
	// map through generateFile so the error is surfaced at codegen time.
	emitted := make(map[string]string) // toolName → "file.proto:Service.Method"
	for _, f := range plugin.Files {
		if !f.Generate {
			continue
		}
		if !hasAnyAnnotatedMethod(f) {
			continue
		}
		if err := generateFile(plugin, f, opts, emitted); err != nil {
			return err
		}
	}
	return nil
}

// toolTemplateData is the context passed to tool.go.tmpl for a single RPC.
// Every identifier-like field is already package-qualified (e.g. "mcp.Tool",
// "greeterv1.HelloRequest") by QualifiedGoIdent so the template does not
// need to know about imports.
type toolTemplateData struct {
	// SchemaVar is the exported name of the package-level var holding the
	// pre-parsed InputSchema (e.g. "_Greeter_SayHello_InputSchema").
	InputSchemaVar  string
	OutputSchemaVar string

	// InputSchemaJSON / OutputSchemaJSON are Go source expressions evaluating
	// to a string of JSON (typically a raw-string literal). The template
	// embeds them verbatim as the argument to protomcp.MustParseSchema.
	InputSchemaJSON  string
	OutputSchemaJSON string

	// ToolName is the fully qualified tool name emitted to the MCP client
	// (e.g. "Greeter_SayHello", possibly with a service-level prefix).
	ToolName string

	// Title / Description are the human-readable strings presented to MCP
	// clients. Either may be empty; the template omits the corresponding
	// field when so.
	Title       string
	Description string

	// Annotations is the literal field list for an &mcp.ToolAnnotations{}
	// struct, or empty when no hints were set.
	AnnotationsLiteral string

	// InputTypeRef / ClientMethod identify the concrete Go types and method
	// the generated handler invokes. Both are already package-qualified.
	InputTypeRef string
	ClientMethod string

	// IsServerStreaming toggles between the unary and server-streaming
	// handler bodies in the template.
	IsServerStreaming bool

	// Qualified identifiers used by the template body. Pre-computed so the
	// template stays free of Go-type-registry lookups.
	QContext           string
	QMCPCallReq        string
	QMCPCallResult     string
	QMCPAddTool        string
	QMCPTool           string
	QJSONRaw           string
	QMetadataMD        string
	QMetadataNewOut    string
	QProtojsonUnm      string
	QProtojsonM        string
	QFmtErrorf         string
	QFmtSprintf        string
	QErrorsIs          string
	QIOEOF             string
	QMCPTextContent    string
	QMCPProgressParams string
	QMCPContent        string

	// QProtomcpGRPCReq is the qualified reference to protomcp.GRPCRequest
	// (e.g. "protomcp.GRPCRequest"), the struct carrying the typed proto
	// input and outgoing gRPC metadata that Middleware can inspect and
	// mutate.
	QProtomcpGRPCReq string

	// QProtomcpClearOutputOnly is the qualified reference to
	// protomcp.ClearOutputOnly, called after protojson.Unmarshal to zero
	// any OUTPUT_ONLY field the client populated.
	QProtomcpClearOutputOnly string
}

// fileTemplateData is the context passed to file.go.tmpl for one *.mcp.pb.go.
type fileTemplateData struct {
	// SourceProtoFile is the path of the input .proto, preserved in the
	// leading "// source:" banner for tool-author readability.
	SourceProtoFile string

	PackageName string

	// ProtomcpPkg is the package alias protogen assigned to the
	// github.com/gdsoumya/protomcp/pkg/protomcp import in this file. It
	// is typically just "protomcp" but may be renamed on collision.
	ProtomcpPkg string

	// PerService groups tools by their owning gRPC service so we can emit
	// one RegisterXxxMCPTools function per service.
	PerService []serviceTemplateData

	// SchemaVars is the flattened list of package-level schema variables,
	// declared once at the top of the file.
	SchemaVars []schemaVar
}

type serviceTemplateData struct {
	// RegisterFuncName is the exported entry point generated code exposes
	// to the user — RegisterGreeterMCPTools, RegisterTasksMCPTools, etc.
	RegisterFuncName string

	// ServiceComment is the (trimmed) leading proto comment on the service,
	// embedded as a doc comment on the register function.
	ServiceComment string

	// ClientTypeRef is the gRPC client interface the user hands in
	// (e.g. "greeterv1.GreeterClient").
	ClientTypeRef string

	// Tools is the list of annotated RPCs (unary + server-streaming) that
	// register as tools in this service.
	Tools []toolTemplateData
}

// schemaVar declares one package-level schema variable. JSON holds a Go
// source expression (typically a raw-string literal) that will be passed to
// protomcp.MustParseSchema.
type schemaVar struct {
	Name string
	JSON string
}

// generateFile is the per-file driver. It computes the target filename and
// Go package (mirroring protoc-gen-go's convention), then builds the
// template context and executes file.go.tmpl into a fresh GeneratedFile.
// emitted is shared across the whole generation run; see GenerateWithOptions.
func generateFile(plugin *protogen.Plugin, f *protogen.File, opts Options, emitted map[string]string) error {
	filename := f.GeneratedFilenamePrefix + ".mcp.pb.go"
	g := plugin.NewGeneratedFile(filename, f.GoImportPath)

	data, err := buildFileTemplateData(g, f, opts, emitted)
	if err != nil {
		return err
	}

	var out strings.Builder
	if err := parsedTemplates.ExecuteTemplate(&out, "file.go.tmpl", data); err != nil {
		return fmt.Errorf("execute file template for %s: %w", f.Desc.Path(), err)
	}
	if _, err := g.Write([]byte(out.String())); err != nil {
		return fmt.Errorf("write generated file %s: %w", filename, err)
	}
	return nil
}

// buildFileTemplateData builds the per-file template context. It also
// registers every import the generated file will need (by qualifying
// identifiers with g.QualifiedGoIdent) and emits the schema vars.
// emitted is a run-wide map of already-used tool names; collisions (within
// or across files) are reported as codegen errors.
func buildFileTemplateData(g *protogen.GeneratedFile, f *protogen.File, opts Options, emitted map[string]string) (*fileTemplateData, error) {
	// Preregister the protomcp runtime import — the template references
	// protomcp.MustParseSchema and *protomcp.Server, and both must resolve
	// to the same package alias.
	protomcpQual := g.QualifiedGoIdent(protogen.GoIdent{GoName: "MustParseSchema", GoImportPath: importProtomcp})
	protomcpPkg := strings.TrimSuffix(protomcpQual, ".MustParseSchema")

	data := &fileTemplateData{
		SourceProtoFile: f.Desc.Path(),
		PackageName:     string(f.GoPackageName),
		ProtomcpPkg:     protomcpPkg,
	}

	for _, svc := range f.Services {
		svcData := serviceTemplateData{
			RegisterFuncName: "Register" + svc.GoName + "MCPTools",
			ClientTypeRef:    g.QualifiedGoIdent(protogen.GoIdent{GoName: svc.GoName + "Client", GoImportPath: f.GoImportPath}),
			ServiceComment:   strings.TrimSpace(schema.CleanComment(string(svc.Comments.Leading))),
		}
		svcOpts := serviceOptionsFor(svc)

		for _, m := range svc.Methods {
			to, ok := toolOptionsFor(m)
			if !ok {
				// Unannotated — skip silently.
				continue
			}
			// Client-streaming and bidi-streaming RPCs have no natural MCP
			// mapping. If the user explicitly annotated them, fail loudly —
			// silently skipping is worse than a clear error. Unannotated
			// streaming RPCs were filtered above by the `ok` check.
			if m.Desc.IsStreamingClient() {
				kind := "client-streaming"
				if m.Desc.IsStreamingServer() {
					kind = "bidi-streaming"
				}
				return nil, fmt.Errorf(
					"%s.%s: %s RPCs cannot be exposed as MCP tools "+
						"(remove the protomcp.v1.tool annotation to suppress this error)",
					svc.GoName, m.GoName, kind,
				)
			}

			tool, err := buildToolTemplateData(g, f, svc, svcOpts, m, to, protomcpPkg, opts)
			if err != nil {
				return nil, err
			}
			if err := validateToolName(tool.ToolName); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
			}
			origin := f.Desc.Path() + ":" + svc.GoName + "." + m.GoName
			if prev, dup := emitted[tool.ToolName]; dup {
				return nil, fmt.Errorf(
					"duplicate MCP tool name %q: declared by both %s and %s "+
						"(adjust tool_prefix or the per-method name override)",
					tool.ToolName, prev, origin,
				)
			}
			emitted[tool.ToolName] = origin
			data.SchemaVars = append(data.SchemaVars,
				schemaVar{Name: tool.InputSchemaVar, JSON: tool.InputSchemaJSON},
				schemaVar{Name: tool.OutputSchemaVar, JSON: tool.OutputSchemaJSON},
			)
			svcData.Tools = append(svcData.Tools, tool)
		}

		// Only emit the per-service Register function if at least one tool
		// survived.
		if len(svcData.Tools) == 0 {
			continue
		}
		data.PerService = append(data.PerService, svcData)
	}

	return data, nil
}

// buildToolTemplateData computes everything the tool.go.tmpl template needs
// for a single annotated RPC, including the JSON schemas (pre-serialized)
// and every package-qualified identifier referenced by the handler body.
func buildToolTemplateData(
	g *protogen.GeneratedFile,
	_ *protogen.File,
	svc *protogen.Service,
	svcOpts *protomcpv1.ServiceOptions,
	m *protogen.Method,
	to *protomcpv1.ToolOptions,
	protomcpPkg string,
	opts Options,
) (toolTemplateData, error) {
	toolName := deriveToolName(svc, svcOpts, m, to)
	baseVar := "_" + svc.GoName + "_" + m.GoName

	schemaOpts := schema.Options{MaxRecursionDepth: opts.MaxRecursionDepth}

	// Build + serialize input/output schemas. We use json.Marshal; maps keys
	// in Go are not deterministic on iteration, but json.Marshal sorts object
	// keys, so the output is stable run-to-run.
	inSchemaJSON, err := marshalSchema(schema.ForInput(m.Input.Desc, schemaOpts))
	if err != nil {
		return toolTemplateData{}, fmt.Errorf("build input schema for %s.%s: %w", svc.GoName, m.GoName, err)
	}
	outSchemaJSON, err := marshalSchema(schema.ForOutput(m.Output.Desc, schemaOpts))
	if err != nil {
		return toolTemplateData{}, fmt.Errorf("build output schema for %s.%s: %w", svc.GoName, m.GoName, err)
	}

	// Qualified identifier helpers.
	q := func(name string, path protogen.GoImportPath) string {
		return g.QualifiedGoIdent(protogen.GoIdent{GoName: name, GoImportPath: path})
	}
	// Pre-qualify the metadata package (so "metadata.MD" and
	// "metadata.NewOutgoingContext" share a single import alias).
	metaMD := q("MD", importGRPCMetadata)
	metaPkg := strings.TrimSuffix(metaMD, ".MD")
	metaNewOut := metaPkg + ".NewOutgoingContext"

	// Likewise for mcp.* references used across the handler body.
	mcpCallReq := q("CallToolRequest", importMCP)
	mcpPkg := strings.TrimSuffix(mcpCallReq, ".CallToolRequest")

	// errors.Is and io.EOF are only used by the server-streaming handler
	// template; registering them unconditionally leaks unused imports into
	// unary-only generated files (which the Go compiler rejects).
	isStream := m.Desc.IsStreamingServer() && !m.Desc.IsStreamingClient()
	var errIs, ioEOF string
	if isStream {
		errIs = q("Is", importErrors)
		ioEOF = q("EOF", importIO)
	}

	return toolTemplateData{
		InputSchemaVar:     baseVar + "_InputSchema",
		OutputSchemaVar:    baseVar + "_OutputSchema",
		InputSchemaJSON:    goRawString(inSchemaJSON),
		OutputSchemaJSON:   goRawString(outSchemaJSON),
		ToolName:           toolName,
		Title:              to.GetTitle(),
		Description:        methodDescription(m, to.GetDescription()),
		AnnotationsLiteral: annotationsLiteral(to, mcpPkg, protomcpPkg),
		InputTypeRef:       g.QualifiedGoIdent(m.Input.GoIdent),
		ClientMethod:       "client." + m.GoName,
		IsServerStreaming:  isStream,

		QContext:                 q("Context", importContext),
		QMCPCallReq:              mcpCallReq,
		QMCPCallResult:           mcpPkg + ".CallToolResult",
		QMCPAddTool:              mcpPkg + ".AddTool",
		QMCPTool:                 mcpPkg + ".Tool",
		QMCPContent:              mcpPkg + ".Content",
		QMCPTextContent:          mcpPkg + ".TextContent",
		QMCPProgressParams:       mcpPkg + ".ProgressNotificationParams",
		QJSONRaw:                 q("RawMessage", importJSON),
		QMetadataMD:              metaMD,
		QMetadataNewOut:          metaNewOut,
		QProtojsonUnm:            q("Unmarshal", importProtojson),
		QProtojsonM:              q("Marshal", importProtojson),
		QFmtErrorf:               q("Errorf", importFmt),
		QFmtSprintf:              q("Sprintf", importFmt),
		QErrorsIs:                errIs,
		QIOEOF:                   ioEOF,
		QProtomcpGRPCReq:         q("GRPCRequest", importProtomcp),
		QProtomcpClearOutputOnly: q("ClearOutputOnly", importProtomcp),
	}, nil
}

// deriveToolName implements the three-step name algorithm documented on
// ToolOptions.Name: explicit override > synthesized <Service>_<Method> >
// service-level prefix applied on top.
func deriveToolName(
	svc *protogen.Service,
	svcOpts *protomcpv1.ServiceOptions,
	m *protogen.Method,
	to *protomcpv1.ToolOptions,
) string {
	var name string
	if n := to.GetName(); n != "" {
		name = n
	} else {
		// svc.GoName and m.GoName are Go identifiers, so they cannot
		// contain characters the MCP spec disallows (dots, slashes,
		// spaces). No rewriting needed; validateToolName enforces the
		// invariant explicitly on the final assembled name.
		name = svc.GoName + "_" + m.GoName
	}
	if svcOpts != nil && svcOpts.GetToolPrefix() != "" {
		// Prefix is applied verbatim so users can pick their own separator
		// (underscore, dash, dot — all legal MCP tool-name characters). The
		// documented convention is to include the separator in the prefix
		// string itself, e.g. tool_prefix: "greeter_".
		name = svcOpts.GetToolPrefix() + name
	}
	return name
}

// validateToolName enforces the MCP spec's tool-name character set
// ([a-zA-Z0-9_.-]) and length bound (128) at codegen time. The SDK
// itself only logs invalid names rather than rejecting them, so we
// catch the problem earlier — a protoc-gen-mcp failure is easier to
// fix than a silent "this tool doesn't work" at runtime.
func validateToolName(s string) error {
	if s == "" {
		return fmt.Errorf("tool name is empty")
	}
	if len(s) > 128 {
		return fmt.Errorf("tool name %q exceeds 128-character limit (%d chars)", s, len(s))
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			// OK
		default:
			return fmt.Errorf("tool name %q contains disallowed character %q (allowed: [a-zA-Z0-9_.-])", s, string(r))
		}
	}
	return nil
}

// annotationsLiteral returns a Go source fragment of the form
// "&mcp.ToolAnnotations{ReadOnlyHint: true, ...}" or the empty string if
// no hints are set. mcpPkg is the already-qualified package alias for the
// mcp import (e.g. "mcp") so that the generated literal uses the exact
// alias protogen assigned.
func annotationsLiteral(to *protomcpv1.ToolOptions, mcpPkg, protomcpPkg string) string {
	var fields []string
	if to.GetReadOnly() {
		fields = append(fields, "ReadOnlyHint: true")
	}
	if to.GetIdempotent() {
		fields = append(fields, "IdempotentHint: true")
	}
	if to.GetDestructive() {
		// DestructiveHint is *bool in the SDK; route through the
		// protomcp.BoolPtr helper so the generated code stays compact.
		fields = append(fields, "DestructiveHint: "+protomcpPkg+".BoolPtr(true)")
	}
	if len(fields) == 0 {
		return ""
	}
	return "&" + mcpPkg + ".ToolAnnotations{" + strings.Join(fields, ", ") + "}"
}

// marshalSchema serializes a schema map to compact JSON. We use compact
// form because the generated Go source already wraps the JSON in a raw
// string literal; indentation would just bloat the binary.
func marshalSchema(m map[string]any) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// goRawString quotes s for embedding in generated Go source. It prefers
// raw string literals (backticks) because they preserve JSON readability.
// If s itself contains a backtick the fallback is strconv.Quote; in
// practice our JSON output never does, but we handle it defensively so a
// future schema change can't silently break codegen.
func goRawString(s string) string {
	if strings.ContainsRune(s, '`') {
		return strconv.Quote(s)
	}
	return "`" + s + "`"
}
