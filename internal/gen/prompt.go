package gen

import (
	"fmt"
	"slices"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// promptTemplateData is the context passed to prompt.go.tmpl.
type promptTemplateData struct {
	PromptName string

	Title       string
	Description string

	// TemplateLiteral is the raw Mustache template string;
	// safeRawString is applied inside the template.
	TemplateLiteral string

	// Arguments lists request-message fields in declaration order.
	Arguments []promptArgument

	// HasCompletions gates RegisterPromptArgCompletions emission.
	HasCompletions bool

	QContext         string
	QMCPGetReq       string
	QMCPGetResult    string
	QMCPPrompt       string
	QMCPPromptArg    string
	QMCPPromptMsg    string
	QMCPTextContent  string
	QMCPContent      string
	QMustacheRender  string
	QFmtErrorf       string
	QJSONUnmarshal   string
	QMetadataMD      string
	QMetadataNewOut  string
	QProtomcpGRPCReq string

	InputTypeRef  string
	OutputTypeRef string
	ClientMethod  string
}

// promptArgument describes a single prompt argument (= request field).
type promptArgument struct {
	// Name is the wire (JSON) name.
	Name string
	// FieldName is the Go field name on the request struct.
	FieldName   string
	Description string
	// Required is set by field_behavior=REQUIRED or
	// buf.validate.required.
	Required bool
	IsEnum   bool
	IsString bool
	// EnumTypeRef is the qualified Go type of the enum.
	EnumTypeRef string
	// EnumMapIdent is the qualified <name>_value map (e.g.
	// "tasksv1.TaskStatus_value") used to resolve names to numeric
	// values.
	EnumMapIdent string
	// CompletionValues lists known completion suggestions (enum
	// values or buf.validate.string.in values).
	CompletionValues []string
}

// buildPromptTemplateData assembles the context for one annotated
// prompt RPC. Streaming RPCs are rejected.
func buildPromptTemplateData(
	g *protogen.GeneratedFile,
	svc *protogen.Service,
	svcOpts *protomcpv1.ServiceOptions,
	m *protogen.Method,
	po *protomcpv1.PromptOptions,
) (promptTemplateData, error) {
	if m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer() {
		var kind string
		switch {
		case m.Desc.IsStreamingClient() && m.Desc.IsStreamingServer():
			kind = "bidi-streaming"
		case m.Desc.IsStreamingClient():
			kind = "client-streaming"
		default:
			kind = "server-streaming"
		}
		return promptTemplateData{}, fmt.Errorf(
			"%s.%s: %s RPCs cannot be exposed as MCP prompts "+
				"(remove the protomcp.v1.prompt annotation to suppress this error)",
			svc.GoName, m.GoName, kind,
		)
	}

	promptName := derivePromptName(svc, svcOpts, m, po)
	if err := validateMCPIdentifier(promptName); err != nil {
		return promptTemplateData{}, fmt.Errorf("%s.%s: prompt name: %w", svc.GoName, m.GoName, err)
	}

	// The Mustache template is rendered after the gRPC call, so every
	// {{path}} must resolve on the RESPONSE message.
	parsed, err := schema.ParseMustache(po.GetTemplate())
	if err != nil {
		return promptTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}
	if vErr := schema.ValidateMustacheFieldPaths(parsed, m.Output.Desc); vErr != nil {
		return promptTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, vErr)
	}

	args, err := buildPromptArguments(g, m.Input)
	if err != nil {
		return promptTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}

	q := func(name string, path protogen.GoImportPath) string {
		return g.QualifiedGoIdent(protogen.GoIdent{GoName: name, GoImportPath: path})
	}
	mcpPkg := strings.TrimSuffix(q("GetPromptRequest", importMCP), ".GetPromptRequest")
	mustachePkg := strings.TrimSuffix(q("Render", importMustache), ".Render")
	metaMD := q("MD", importGRPCMetadata)
	metaPkg := strings.TrimSuffix(metaMD, ".MD")

	hasCompletions := false
	for _, a := range args {
		if len(a.CompletionValues) > 0 {
			hasCompletions = true
			break
		}
	}

	return promptTemplateData{
		PromptName:      promptName,
		Title:           po.GetTitle(),
		Description:     methodDescription(m, po.GetDescription()),
		TemplateLiteral: safeRawString(po.GetTemplate()),
		Arguments:       args,
		HasCompletions:  hasCompletions,

		QContext:         q("Context", importContext),
		QMCPGetReq:       mcpPkg + ".GetPromptRequest",
		QMCPGetResult:    mcpPkg + ".GetPromptResult",
		QMCPPrompt:       mcpPkg + ".Prompt",
		QMCPPromptArg:    mcpPkg + ".PromptArgument",
		QMCPPromptMsg:    mcpPkg + ".PromptMessage",
		QMCPTextContent:  mcpPkg + ".TextContent",
		QMCPContent:      mcpPkg + ".Content",
		QMustacheRender:  mustachePkg + ".Render",
		QFmtErrorf:       q("Errorf", importFmt),
		QJSONUnmarshal:   q("Unmarshal", importJSON),
		QMetadataMD:      metaMD,
		QMetadataNewOut:  metaPkg + ".NewOutgoingContext",
		QProtomcpGRPCReq: q("GRPCData", importProtomcp),

		InputTypeRef:  g.QualifiedGoIdent(m.Input.GoIdent),
		OutputTypeRef: g.QualifiedGoIdent(m.Output.GoIdent),
		ClientMethod:  "client." + m.GoName,
	}, nil
}

// derivePromptName returns the explicit override or
// <Service>_<Method>. Service-level tool_prefix does NOT apply to
// prompt names (separate namespace per MCP spec).
func derivePromptName(
	svc *protogen.Service,
	_ *protomcpv1.ServiceOptions,
	m *protogen.Method,
	po *protomcpv1.PromptOptions,
) string {
	if n := po.GetName(); n != "" {
		return n
	}
	return svc.GoName + "_" + m.GoName
}

// buildPromptArguments walks the request message and returns one
// promptArgument per user-settable field. MCP arguments are
// map[string]string, so only string and enum fields are accepted.
func buildPromptArguments(g *protogen.GeneratedFile, in *protogen.Message) ([]promptArgument, error) {
	args := make([]promptArgument, 0, len(in.Fields))
	for _, f := range in.Fields {
		fd := f.Desc
		if fd.IsList() || fd.IsMap() {
			return nil, fmt.Errorf("prompt arg %q on %s: repeated/map request fields are not supported; use a scalar or enum", fd.Name(), in.Desc.FullName())
		}
		arg := promptArgument{
			Name:        fd.JSONName(),
			FieldName:   f.GoName,
			Description: strings.TrimSpace(schema.CleanComment(string(f.Comments.Leading))),
			Required:    isFieldRequired(fd),
		}
		switch fd.Kind() {
		case protoreflect.StringKind:
			arg.IsString = true
			if vals := stringInValues(fd); len(vals) > 0 {
				arg.CompletionValues = vals
			}
		case protoreflect.EnumKind:
			arg.IsEnum = true
			enumGo := enumGoIdent(g, f)
			arg.EnumTypeRef = enumGo
			arg.EnumMapIdent = enumGo + "_value"
			arg.CompletionValues = enumValueNames(fd.Enum())
		default:
			return nil, fmt.Errorf("prompt arg %q on %s: kind %s is not supported; use string or enum", fd.Name(), in.Desc.FullName(), fd.Kind())
		}
		args = append(args, arg)
	}
	return args, nil
}

// enumGoIdent returns the qualified Go identifier of an enum field's
// generated type.
func enumGoIdent(g *protogen.GeneratedFile, f *protogen.Field) string {
	return g.QualifiedGoIdent(f.Enum.GoIdent)
}

// enumValueNames returns enum value names in order with the
// _UNSPECIFIED sentinel (numeric 0, or suffix "_UNSPECIFIED" per
// AIP-126) removed.
func enumValueNames(ed protoreflect.EnumDescriptor) []string {
	values := ed.Values()
	out := make([]string, 0, values.Len())
	for i := range values.Len() {
		v := values.Get(i)
		name := string(v.Name())
		if v.Number() == 0 || strings.HasSuffix(name, "_UNSPECIFIED") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// stringInValues returns the buf.validate.string.in list for fd, or
// nil.
func stringInValues(fd protoreflect.FieldDescriptor) []string {
	if !proto.HasExtension(fd.Options(), validate.E_Field) {
		return nil
	}
	rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
	if rules == nil {
		return nil
	}
	s := rules.GetString()
	if s == nil {
		return nil
	}
	return s.GetIn()
}

// isFieldRequired reports whether fd carries
// google.api.field_behavior=REQUIRED or buf.validate.field.required.
func isFieldRequired(fd protoreflect.FieldDescriptor) bool {
	if proto.HasExtension(fd.Options(), annotations.E_FieldBehavior) {
		behaviors, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
		if slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED) {
			return true
		}
	}
	if proto.HasExtension(fd.Options(), validate.E_Field) {
		rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
		if rules != nil && rules.GetRequired() {
			return true
		}
	}
	return false
}
