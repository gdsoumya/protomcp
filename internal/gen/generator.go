package gen

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// Import paths of every external package the generated file references.
const (
	importContext      = protogen.GoImportPath("context")
	importJSON         = protogen.GoImportPath("encoding/json")
	importErrors       = protogen.GoImportPath("errors")
	importFmt          = protogen.GoImportPath("fmt")
	importIO           = protogen.GoImportPath("io")
	importProtomcp     = protogen.GoImportPath("github.com/gdsoumya/protomcp/pkg/protomcp")
	importMCP          = protogen.GoImportPath("github.com/modelcontextprotocol/go-sdk/mcp")
	importGRPCMetadata = protogen.GoImportPath("google.golang.org/grpc/metadata")
	importURITemplate  = protogen.GoImportPath("github.com/yosida95/uritemplate/v3")
	importMustache     = protogen.GoImportPath("github.com/cbroglie/mustache")
)

// Options controls generator behavior tunable from plugin flags.
type Options struct {
	// MaxRecursionDepth caps recursive message expansion per path. Zero
	// uses the library default (schema.defaultMaxRecursionDepth).
	// Exposed as -max_recursion_depth=N on protoc-gen-mcp.
	MaxRecursionDepth int
}

// Generate is the protogen entry point.
func Generate(plugin *protogen.Plugin) error {
	return GenerateWithOptions(plugin, Options{})
}

// GenerateWithOptions is the configurable entry point.
func GenerateWithOptions(plugin *protogen.Plugin, opts Options) error {
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	// mcp.AddTool silently replaces an existing tool with the same
	// name, so cross-file collisions are caught here at codegen time.
	emitted := make(map[string]string) // toolName → "file.proto:Service.Method"

	// At most one resource_list annotation per run: MCP `resources/list`
	// is a single flat cursor-paginated stream, so multi-lister
	// fan-out would break pagination semantics.
	var resourceListSite string

	// At most one resource_list_changed annotation per run: every one
	// fires the same `notifications/resources/list_changed` wire event.
	var resourceListChangedSite string

	for _, f := range plugin.Files {
		if !f.Generate {
			continue
		}
		if !hasAnyAnnotatedMethod(f) {
			continue
		}
		if err := generateFile(plugin, f, opts, emitted, &resourceListSite, &resourceListChangedSite); err != nil {
			return err
		}
	}
	return nil
}

// toolTemplateData is the context passed to tool.go.tmpl.
type toolTemplateData struct {
	InputSchemaVar  string
	OutputSchemaVar string

	// InputSchemaJSON / OutputSchemaJSON are Go source expressions
	// evaluating to a string of JSON (usually a raw-string literal).
	InputSchemaJSON  string
	OutputSchemaJSON string

	ToolName string

	Title       string
	Description string

	// AnnotationsLiteral is the literal field list for an
	// &mcp.ToolAnnotations{} struct, or empty when no hints were set.
	AnnotationsLiteral string

	InputTypeRef string
	ClientMethod string

	IsServerStreaming bool

	commonQuals

	QMCPCallReq        string
	QMCPCallResult     string
	QMCPAddTool        string
	QMCPTool           string
	QJSONRaw           string
	QMetadataMD        string
	QMetadataNewOut    string
	QMCPTextContent    string
	QMCPProgressParams string
	QMCPContent        string

	QProtomcpGRPCReq string

	// QProtomcpClearOutputOnly zeros OUTPUT_ONLY fields after
	// protojson.Unmarshal.
	QProtomcpClearOutputOnly string

	// QProtomcpSanitizeMetadataValue strips CR/LF/NUL from
	// client-controlled strings before they land in outgoing gRPC
	// metadata, preventing log-line forgery and HPACK trips at the
	// upstream hop.
	QProtomcpSanitizeMetadataValue string

	// Elicitation is non-nil when the method also carries a
	// protomcp.v1.elicitation annotation; the tool template then emits
	// a session.Elicit(...) call before the upstream gRPC invocation.
	Elicitation *elicitationTemplateData
}

// elicitationTemplateData carries the per-RPC context the tool template
// needs to emit a session.Elicit call.
type elicitationTemplateData struct {
	// MessageExpr is a Go source expression that evaluates at runtime
	// to the fully-rendered confirmation prompt. For literal strings
	// it is a quoted string constant; for Mustache-templated messages
	// it is a concatenation of literal segments and fmt.Sprintf("%v",
	// ...) field getters.
	MessageExpr string

	QMCPElicitParams string
}

// commonQuals bundles qualified identifiers every template site needs.
type commonQuals struct {
	QContext    string
	QFmtErrorf  string
	QFmtSprintf string
	QErrorsIs   string
	QIOEOF      string
}

// fileTemplateData is the context passed to file.go.tmpl.
type fileTemplateData struct {
	SourceProtoFile string

	PackageName string

	ProtomcpPkg string

	MCPPkg string

	URITemplatePkg string

	MustachePkg string

	commonQuals

	QContextCancel     string
	QContextWithCancel string
	JSONUnmarshal      string

	QMetadataMD string

	PerService []serviceTemplateData

	SchemaVars []schemaVar

	URITemplateVars []uriTemplateVar
}

// uriTemplateVar declares one package-level *uritemplate.Template var.
type uriTemplateVar struct {
	Name string
	Raw  string
}

type serviceTemplateData struct {
	RegisterFuncName string

	RegisterResourcesFuncName string

	PromptsRegisterFuncName string

	// StartListChangedWatchersFuncName is the generated
	// Start<Svc>MCPResourceListChangedWatchers function. Emitted only
	// when the service has at least one resource_list_changed
	// annotation.
	StartListChangedWatchersFuncName string

	ServiceComment string

	ClientTypeRef string

	Tools []toolTemplateData

	ResourceReads []resourceReadTemplateData
	ResourceLists []resourceListTemplateData

	HasResources bool

	ResourceListChangedWatchers []resourceListChangedWatcherData

	Prompts []promptTemplateData
}

// resourceListChangedWatcherData drives one watcher entry inside the
// Start<Svc>MCPResourceListChangedWatchers function.
type resourceListChangedWatcherData struct {
	RPCName        string
	ClientMethod   string
	RequestTypeRef string
}

// schemaVar declares one package-level schema variable.
type schemaVar struct {
	Name string
	JSON string
}

// generateFile is the per-file driver.
func generateFile(plugin *protogen.Plugin, f *protogen.File, opts Options, emitted map[string]string, resourceListSite, resourceListChangedSite *string) error {
	filename := f.GeneratedFilenamePrefix + ".mcp.pb.go"
	g := plugin.NewGeneratedFile(filename, f.GoImportPath)

	data, err := buildFileTemplateData(g, f, opts, emitted, resourceListSite, resourceListChangedSite)
	if err != nil {
		return err
	}

	var out strings.Builder
	if err := parsedTemplates.ExecuteTemplate(&out, "file.go.tmpl", data); err != nil {
		return fmt.Errorf("execute file template for %s: %w", f.Desc.Path(), err)
	}
	source := out.String()
	// Parse as Go source before handing to protogen so template bugs
	// surface here with proto-file context rather than as opaque build
	// failures on the user's side.
	if err := validateGeneratedGo(f.Desc.Path(), filename, source); err != nil {
		return err
	}
	if _, err := g.Write([]byte(source)); err != nil {
		return fmt.Errorf("write generated file %s: %w", filename, err)
	}
	return nil
}

// validateGeneratedGo parses src as Go source and returns a diagnostic
// error naming the proto path, Go filename, and a source excerpt around
// the first parse error. protoPath may be empty (unit-test callers).
func validateGeneratedGo(protoPath, goFilename, src string) error {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, goFilename, src, parser.ParseComments); err != nil {
		line := firstErrorLine(err)
		excerpt := sourceExcerpt(src, line, 15)
		origin := protoPath
		if origin == "" {
			origin = "<unknown>"
		}
		return fmt.Errorf(
			"protoc-gen-mcp: generated Go is not parseable\n"+
				"  proto:  %s\n"+
				"  target: %s\n"+
				"  parser: %v\n"+
				"--- generated source around line %d ---\n%s",
			origin, goFilename, err, line, excerpt,
		)
	}
	return nil
}

// firstErrorLine extracts the first scanner.Error's line number from a
// go/parser error. Returns 0 if no positional information is available.
func firstErrorLine(err error) int {
	var list scanner.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		return list[0].Pos.Line
	}
	return 0
}

// sourceExcerpt returns up to radius lines on either side of line
// (1-indexed) from src, each prefixed with its line number. When line
// is zero the first 2*radius lines are returned.
func sourceExcerpt(src string, line, radius int) string {
	lines := strings.Split(src, "\n")
	if len(lines) == 0 {
		return ""
	}
	start, end := 1, len(lines)
	if line > 0 {
		start = line - radius
		if start < 1 {
			start = 1
		}
		end = line + radius
		if end > len(lines) {
			end = len(lines)
		}
	} else if 2*radius < end {
		end = 2 * radius
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%5d | %s\n", i, lines[i-1])
	}
	return b.String()
}

// buildFileTemplateData builds the per-file template context and
// registers every import the generated file will need.
func buildFileTemplateData(g *protogen.GeneratedFile, f *protogen.File, opts Options, emitted map[string]string, resourceListSite, resourceListChangedSite *string) (*fileTemplateData, error) {
	// Preregister the protomcp runtime import so protomcp.MustParseSchema
	// and *protomcp.Server resolve to the same alias.
	protomcpQual := g.QualifiedGoIdent(protogen.GoIdent{GoName: "MustParseSchema", GoImportPath: importProtomcp})
	protomcpPkg := strings.TrimSuffix(protomcpQual, ".MustParseSchema")

	q := func(name string, path protogen.GoImportPath) string {
		return g.QualifiedGoIdent(protogen.GoIdent{GoName: name, GoImportPath: path})
	}

	data := &fileTemplateData{
		SourceProtoFile: f.Desc.Path(),
		PackageName:     string(f.GoPackageName),
		ProtomcpPkg:     protomcpPkg,
		commonQuals: commonQuals{
			QContext:    q("Context", importContext),
			QFmtErrorf:  q("Errorf", importFmt),
			QFmtSprintf: q("Sprintf", importFmt),
			// QErrorsIs / QIOEOF populated later only for streaming.
		},
		QContextCancel:     q("CancelFunc", importContext),
		QContextWithCancel: q("WithCancel", importContext),
	}

	// uriTemplateByRaw deduplicates URI templates within a file so
	// `resource` and matching `resource_list` share one var.
	uriTemplateByRaw := map[string]string{}
	allocURITemplateVar := func(raw string) string {
		if v, ok := uriTemplateByRaw[raw]; ok {
			return v
		}
		name := fmt.Sprintf("_protomcp_URITemplate_%d", len(uriTemplateByRaw))
		uriTemplateByRaw[raw] = name
		data.URITemplateVars = append(data.URITemplateVars, uriTemplateVar{Name: name, Raw: raw})
		return name
	}

	// Resource-related imports are registered only when a resource
	// surface exists, so tool-only files stay lean.
	anyResourceEmitted := false

	// Prompts have a separate namespace from tools per MCP spec.
	promptEmitted := make(map[string]string)

	for _, svc := range f.Services {
		svcData := serviceTemplateData{
			RegisterFuncName:                 "Register" + svc.GoName + "MCPTools",
			RegisterResourcesFuncName:        "Register" + svc.GoName + "MCPResources",
			PromptsRegisterFuncName:          "Register" + svc.GoName + "MCPPrompts",
			StartListChangedWatchersFuncName: "Start" + svc.GoName + "MCPResourceListChangedWatchers",
			ClientTypeRef:                    g.QualifiedGoIdent(protogen.GoIdent{GoName: svc.GoName + "Client", GoImportPath: f.GoImportPath}),
			ServiceComment:                   strings.TrimSpace(schema.CleanComment(string(svc.Comments.Leading))),
		}
		svcOpts := serviceOptionsFor(svc)

		for _, m := range svc.Methods {
			_, hasElicit := elicitationOptionsFor(m)
			if !hasAnyPrimitiveAnnotation(m) {
				// Surface standalone elicitation before the silent
				// skip so authoring mistakes do not become no-ops.
				if hasElicit {
					return nil, fmt.Errorf(
						"%s.%s: protomcp.v1.elicitation requires a protomcp.v1.tool "+
							"annotation on the same method; elicitation has no meaning "+
							"without a tool to gate",
						svc.GoName, m.GoName,
					)
				}
				continue
			}
			class := classifyMethod(m)

			if hasElicit && !class.asTool {
				return nil, fmt.Errorf(
					"%s.%s: protomcp.v1.elicitation requires a protomcp.v1.tool "+
						"annotation on the same method; elicitation has no meaning "+
						"without a tool to gate",
					svc.GoName, m.GoName,
				)
			}

			// Client-/bidi-streaming have no sensible MCP mapping.
			if m.Desc.IsStreamingClient() {
				kind := "client-streaming"
				if m.Desc.IsStreamingServer() {
					kind = "bidi-streaming"
				}
				return nil, fmt.Errorf(
					"%s.%s: %s RPCs cannot be exposed as MCP primitives "+
						"(remove the protomcp.v1 annotation to suppress this error)",
					svc.GoName, m.GoName, kind,
				)
			}
			// Server-streaming + elicitation would pause a stream on a
			// synchronous prompt; reject to avoid flow-control surprises.
			if hasElicit && m.Desc.IsStreamingServer() {
				return nil, fmt.Errorf(
					"%s.%s: protomcp.v1.elicitation is not supported on "+
						"server-streaming RPCs",
					svc.GoName, m.GoName,
				)
			}

			if m.Desc.IsStreamingServer() && (class.asResourceTemplate || class.asResourceList) {
				return nil, fmt.Errorf(
					"%s.%s: server-streaming RPC cannot carry protomcp.v1.resource "+
						"or protomcp.v1.resource_list (those require unary RPCs)",
					svc.GoName, m.GoName,
				)
			}
			if !m.Desc.IsStreamingServer() && class.asResourceListChanged {
				return nil, fmt.Errorf(
					"%s.%s: protomcp.v1.resource_list_changed requires a "+
						"server-streaming RPC (the generator opens the stream "+
						"and fires Server.NotifyResourceListChanged() per event)",
					svc.GoName, m.GoName,
				)
			}

			if class.asTool {
				to, _ := toolOptionsFor(m)
				tool, err := buildToolTemplateData(g, f, svc, svcOpts, m, to, protomcpPkg, opts)
				if err != nil {
					return nil, err
				}
				if hasElicit {
					eo, _ := elicitationOptionsFor(m)
					etd, etErr := buildElicitationTemplateData(g, m, eo, tool.QFmtSprintf)
					if etErr != nil {
						return nil, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, etErr)
					}
					tool.Elicitation = etd
				}
				if err := validateMCPIdentifier(tool.ToolName); err != nil {
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

			if class.asResourceTemplate {
				ro, _ := resourceTemplateOptionsFor(m)
				rd, err := buildResourceReadTemplateData(g, svc, m, ro)
				if err != nil {
					return nil, err
				}
				rd.URITemplateVar = allocURITemplateVar(rd.URITemplate)
				svcData.ResourceReads = append(svcData.ResourceReads, rd)
				anyResourceEmitted = true
			}
			if class.asResourceList {
				rlo, _ := resourceListOptionsFor(m)
				site := fmt.Sprintf("%s:%s.%s", f.Desc.Path(), svc.GoName, m.GoName)
				if *resourceListSite != "" {
					return nil, fmt.Errorf(
						"%s: protomcp.v1.resource_list is already registered by "+
							"%s; at most one resource_list annotation is allowed "+
							"per generation run. `resources/list` is a single "+
							"flat cursor-paginated stream in the MCP spec; "+
							"consolidate into one gRPC RPC and template the URI "+
							"scheme (e.g. \"{type}://{id}\") to enumerate "+
							"multiple resource types",
						site, *resourceListSite,
					)
				}
				*resourceListSite = site
				ld, err := buildResourceListTemplateData(g, svc, m, rlo)
				if err != nil {
					return nil, err
				}
				ld.URITemplateVar = allocURITemplateVar(ld.URITemplate)
				svcData.ResourceLists = append(svcData.ResourceLists, ld)
				anyResourceEmitted = true
			}
			if class.asResourceListChanged {
				site := fmt.Sprintf("%s:%s.%s", f.Desc.Path(), svc.GoName, m.GoName)
				if *resourceListChangedSite != "" {
					return nil, fmt.Errorf(
						"%s: protomcp.v1.resource_list_changed is already "+
							"registered by %s; at most one resource_list_changed "+
							"annotation is allowed per generation run. Every "+
							"annotation fires the same `notifications/resources/list_changed` "+
							"wire event, there is no per-annotation differentiation. "+
							"If events come from multiple backend feeds, "+
							"consolidate them into one server-streaming RPC "+
							"gRPC-side (same pattern as resource_list)",
						site, *resourceListChangedSite,
					)
				}
				*resourceListChangedSite = site
				svcData.ResourceListChangedWatchers = append(svcData.ResourceListChangedWatchers,
					resourceListChangedWatcherData{
						RPCName:        m.GoName,
						ClientMethod:   "client." + m.GoName,
						RequestTypeRef: g.QualifiedGoIdent(m.Input.GoIdent),
					})
			}
			if class.asPrompt {
				po, _ := promptOptionsFor(m)
				prompt, err := buildPromptTemplateData(g, svc, svcOpts, m, po)
				if err != nil {
					return nil, err
				}
				origin := f.Desc.Path() + ":" + svc.GoName + "." + m.GoName
				if prev, dup := promptEmitted[prompt.PromptName]; dup {
					return nil, fmt.Errorf(
						"duplicate MCP prompt name %q: declared by both %s and %s "+
							"(set an explicit name on protomcp.v1.prompt to disambiguate)",
						prompt.PromptName, prev, origin,
					)
				}
				promptEmitted[prompt.PromptName] = origin
				svcData.Prompts = append(svcData.Prompts, prompt)
			}
		}

		svcData.HasResources = len(svcData.ResourceReads) > 0 ||
			len(svcData.ResourceLists) > 0

		if len(svcData.Tools) == 0 && !svcData.HasResources && len(svcData.Prompts) == 0 {
			continue
		}
		data.PerService = append(data.PerService, svcData)
	}

	if anyResourceEmitted {
		// Referencing a real symbol (Resource) prevents protogen from
		// pruning the import and keeps the alias consistent with the
		// tool side.
		mcpRes := q("Resource", importMCP)
		data.MCPPkg = strings.TrimSuffix(mcpRes, ".Resource")
		uriNew := q("New", importURITemplate)
		data.URITemplatePkg = strings.TrimSuffix(uriNew, ".New")
		musRender := q("Render", importMustache)
		data.MustachePkg = strings.TrimSuffix(musRender, ".Render")
		data.JSONUnmarshal = q("Unmarshal", importJSON)
		data.QMetadataMD = q("MD", importGRPCMetadata)
	}

	return data, nil
}

// buildToolTemplateData computes the tool.go.tmpl context for a single
// annotated RPC, including serialized JSON schemas and every
// package-qualified identifier referenced by the handler body.
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

	// json.Marshal sorts object keys so output is stable run-to-run.
	inSchemaJSON, err := marshalSchema(schema.ForInput(m.Input.Desc, schemaOpts))
	if err != nil {
		return toolTemplateData{}, fmt.Errorf("build input schema for %s.%s: %w", svc.GoName, m.GoName, err)
	}
	outSchemaJSON, err := marshalSchema(schema.ForOutput(m.Output.Desc, schemaOpts))
	if err != nil {
		return toolTemplateData{}, fmt.Errorf("build output schema for %s.%s: %w", svc.GoName, m.GoName, err)
	}

	q := func(name string, path protogen.GoImportPath) string {
		return g.QualifiedGoIdent(protogen.GoIdent{GoName: name, GoImportPath: path})
	}
	// Share one import alias across metadata.MD / NewOutgoingContext.
	metaMD := q("MD", importGRPCMetadata)
	metaPkg := strings.TrimSuffix(metaMD, ".MD")
	metaNewOut := metaPkg + ".NewOutgoingContext"

	mcpCallReq := q("CallToolRequest", importMCP)
	mcpPkg := strings.TrimSuffix(mcpCallReq, ".CallToolRequest")

	// errors.Is and io.EOF are only used by the streaming template;
	// unconditional registration leaks unused imports.
	isStream := m.Desc.IsStreamingServer() && !m.Desc.IsStreamingClient()
	var errIs, ioEOF string
	if isStream {
		errIs = q("Is", importErrors)
		ioEOF = q("EOF", importIO)
	}

	return toolTemplateData{
		InputSchemaVar:     baseVar + "_InputSchema",
		OutputSchemaVar:    baseVar + "_OutputSchema",
		InputSchemaJSON:    safeRawString(inSchemaJSON),
		OutputSchemaJSON:   safeRawString(outSchemaJSON),
		ToolName:           toolName,
		Title:              to.GetTitle(),
		Description:        methodDescription(m, to.GetDescription()),
		AnnotationsLiteral: annotationsLiteral(to, mcpPkg, protomcpPkg),
		InputTypeRef:       g.QualifiedGoIdent(m.Input.GoIdent),
		ClientMethod:       "client." + m.GoName,
		IsServerStreaming:  isStream,

		commonQuals: commonQuals{
			QContext:    q("Context", importContext),
			QFmtErrorf:  q("Errorf", importFmt),
			QFmtSprintf: q("Sprintf", importFmt),
			QErrorsIs:   errIs,
			QIOEOF:      ioEOF,
		},
		QMCPCallReq:                    mcpCallReq,
		QMCPCallResult:                 mcpPkg + ".CallToolResult",
		QMCPAddTool:                    mcpPkg + ".AddTool",
		QMCPTool:                       mcpPkg + ".Tool",
		QMCPContent:                    mcpPkg + ".Content",
		QMCPTextContent:                mcpPkg + ".TextContent",
		QMCPProgressParams:             mcpPkg + ".ProgressNotificationParams",
		QJSONRaw:                       q("RawMessage", importJSON),
		QMetadataMD:                    metaMD,
		QMetadataNewOut:                metaNewOut,
		QProtomcpGRPCReq:               q("GRPCData", importProtomcp),
		QProtomcpClearOutputOnly:       q("ClearOutputOnly", importProtomcp),
		QProtomcpSanitizeMetadataValue: q("SanitizeMetadataValue", importProtomcp),
	}, nil
}

// buildElicitationTemplateData validates the elicitation annotation and
// computes the tool template context for the session.Elicit(...) gate.
// Validation rejects empty messages, unresolved Mustache variables, and
// unsupported Mustache forms (sections, partials).
func buildElicitationTemplateData(
	g *protogen.GeneratedFile,
	m *protogen.Method,
	eo *protomcpv1.ElicitationOptions,
	qFmtSprintf string,
) (*elicitationTemplateData, error) {
	msg := eo.GetMessage()
	if msg == "" {
		return nil, fmt.Errorf(
			"protomcp.v1.elicitation.message is required; an elicitation " +
				"with no prompt has nothing to show the user",
		)
	}
	parsed, err := schema.ParseMustache(msg)
	if err != nil {
		return nil, fmt.Errorf("protomcp.v1.elicitation.message: %w", err)
	}
	if verr := schema.ValidateMustacheFieldPaths(parsed, m.Input.Desc); verr != nil {
		return nil, fmt.Errorf("protomcp.v1.elicitation.message: %w", verr)
	}
	vars := locateMustacheVars(msg)
	// Reading via (&in) composes nil-safe getter chains over a
	// partially populated request.
	expr, err := renderMustacheGoExpr(msg, vars, m.Input.Desc, "(&in)", qFmtSprintf)
	if err != nil {
		return nil, fmt.Errorf("protomcp.v1.elicitation.message: %w", err)
	}
	qElicitParams := g.QualifiedGoIdent(protogen.GoIdent{
		GoName:       "ElicitParams",
		GoImportPath: importMCP,
	})
	return &elicitationTemplateData{
		MessageExpr:      expr,
		QMCPElicitParams: qElicitParams,
	}, nil
}

// deriveToolName implements the ToolOptions.Name algorithm: explicit
// override > synthesized <Service>_<Method> > service-level prefix
// applied on top.
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
		name = svc.GoName + "_" + m.GoName
	}
	if svcOpts != nil && svcOpts.GetToolPrefix() != "" {
		// Prefix is verbatim; the separator is part of the prefix
		// string itself (e.g. tool_prefix: "greeter_").
		name = svcOpts.GetToolPrefix() + name
	}
	return name
}

// validateMCPIdentifier enforces the MCP spec's identifier character
// set ([a-zA-Z0-9_.-]) and 128-character length bound at codegen time.
// The SDK only logs invalid names at runtime, so catching them here
// avoids silent tool-loss.
func validateMCPIdentifier(s string) error {
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
// "&mcp.ToolAnnotations{ReadOnlyHint: true, ...}" or "" if no hints
// are set.
func annotationsLiteral(to *protomcpv1.ToolOptions, mcpPkg, protomcpPkg string) string {
	var fields []string
	if to.GetReadOnly() {
		fields = append(fields, "ReadOnlyHint: true")
	}
	if to.GetIdempotent() {
		fields = append(fields, "IdempotentHint: true")
	}
	if to.GetDestructive() {
		// DestructiveHint is *bool in the SDK; BoolPtr keeps the
		// emitted code compact.
		fields = append(fields, "DestructiveHint: "+protomcpPkg+".BoolPtr(true)")
	}
	if to.GetOpenWorld() {
		fields = append(fields, "OpenWorldHint: "+protomcpPkg+".BoolPtr(true)")
	}
	if len(fields) == 0 {
		return ""
	}
	return "&" + mcpPkg + ".ToolAnnotations{" + strings.Join(fields, ", ") + "}"
}

// marshalSchema serializes a schema map to compact JSON.
func marshalSchema(m map[string]any) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// safeRawString quotes s as a Go expression that evaluates to s. Raw
// strings (backticks) are preferred for readability; embedded backticks
// are stitched in as double-quoted segments joined with "+", e.g.
//
//	`foo` + "`" + `bar`
func safeRawString(s string) string {
	if s == "" {
		return "``"
	}
	if !strings.ContainsRune(s, '`') {
		return "`" + s + "`"
	}
	var b strings.Builder
	i, n := 0, len(s)
	first := true
	emit := func(piece string) {
		if !first {
			b.WriteString(" + ")
		}
		b.WriteString(piece)
		first = false
	}
	for i < n {
		j := i
		for j < n && s[j] != '`' {
			j++
		}
		if j > i {
			emit("`" + s[i:j] + "`")
			i = j
		}
		j = i
		for j < n && s[j] == '`' {
			j++
		}
		if j > i {
			emit("\"" + strings.Repeat("`", j-i) + "\"")
			i = j
		}
	}
	return b.String()
}
