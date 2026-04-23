package gen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
	protomcpv1 "github.com/gdsoumya/protomcp/pkg/api/gen/protomcp/v1"
)

// resourceReadTemplateData drives resource_read.go.tmpl.
type resourceReadTemplateData struct {
	URITemplate string
	// URITemplateVar is the package-level *uritemplate.Template var
	// shared across `resource` and `resource_list` annotations that
	// reference the same raw template.
	URITemplateVar string
	// Name / Description are raw Mustache sources rendered at request
	// time against the response.
	Name            string
	Description     string
	MIMEType        string
	BlobFieldGetter string // "Out.GetFoo().GetBar()" or "" when no blob mode
	Bindings        []resourceBinding

	InputTypeRef  string
	OutputTypeRef string
	ClientMethod  string

	// UniqueName is the Service_Method identifier used to synthesize
	// package-level var names.
	UniqueName string
}

type resourceBinding struct {
	Placeholder string
	// FieldAssign is a Go statement (newline-terminated) that assigns
	// the local `val` string to the bound proto field on `in`.
	FieldAssign string
}

// resourceListTemplateData drives resource_list.go.tmpl.
type resourceListTemplateData struct {
	URITemplate    string
	URITemplateVar string
	Name           string
	Description    string
	MIMEType       string
	// ItemFieldGetter is a chained Go getter returning the []*Item
	// slice on a response message, e.g. `resp.GetTasks()`.
	ItemFieldGetter string
	ItemBindings    []resourceListBinding

	InputTypeRef  string
	OutputTypeRef string
	ClientMethod  string

	UniqueName string
}

type resourceListBinding struct {
	Placeholder string
	// FieldGetter is a Go expression yielding a string for the binding
	// given a variable `item`, e.g. `item.GetId()`.
	FieldGetter string
}

// buildResourceReadTemplateData compiles the resource_read.go.tmpl
// context from a `resource` annotation and the RPC descriptors.
func buildResourceReadTemplateData(
	g *protogen.GeneratedFile,
	svc *protogen.Service,
	m *protogen.Method,
	ro *protomcpv1.ResourceTemplateOptions,
) (resourceReadTemplateData, error) {
	if m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer() {
		return resourceReadTemplateData{}, fmt.Errorf(
			"%s.%s: protomcp.v1.resource requires a unary RPC "+
				"(client-/server-/bidi-streaming is not supported)",
			svc.GoName, m.GoName,
		)
	}

	uriTemplate := ro.GetUriTemplate()
	if uriTemplate == "" {
		return resourceReadTemplateData{}, fmt.Errorf(
			"%s.%s: protomcp.v1.resource.uri_template is empty",
			svc.GoName, m.GoName,
		)
	}
	_, varNames, err := schema.ParseURITemplate(uriTemplate)
	if err != nil {
		return resourceReadTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}

	bindings := bindingMap(ro.GetUriBindings())
	if err := schema.ValidatePlaceholderBindings(varNames, bindings, m.Input.Desc); err != nil {
		return resourceReadTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}

	// Mustache templates are validated against the RESPONSE message.
	if nf := ro.GetNameField(); nf != "" {
		tmpl, err := schema.ParseMustache(nf)
		if err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: name_field: %w", svc.GoName, m.GoName, err)
		}
		if err := schema.ValidateMustacheFieldPaths(tmpl, m.Output.Desc); err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: name_field: %w", svc.GoName, m.GoName, err)
		}
	}
	if df := ro.GetDescriptionField(); df != "" {
		tmpl, err := schema.ParseMustache(df)
		if err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: description_field: %w", svc.GoName, m.GoName, err)
		}
		if err := schema.ValidateMustacheFieldPaths(tmpl, m.Output.Desc); err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: description_field: %w", svc.GoName, m.GoName, err)
		}
	}

	mime := ro.GetMimeType()
	blobGetter := ""
	if bf := ro.GetBlobField(); bf != "" {
		if mime == "" {
			return resourceReadTemplateData{}, fmt.Errorf(
				"%s.%s: blob_field %q requires mime_type to be set",
				svc.GoName, m.GoName, bf,
			)
		}
		getter, err := messageFieldGetter(m.Output.Desc, bf, "Out")
		if err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: blob_field: %w", svc.GoName, m.GoName, err)
		}
		fd := schema.FindMessageField(m.Output.Desc, bf)
		if fd == nil || fd.Kind() != protoreflect.BytesKind {
			return resourceReadTemplateData{}, fmt.Errorf(
				"%s.%s: blob_field %q must point at a bytes field",
				svc.GoName, m.GoName, bf,
			)
		}
		blobGetter = getter
	}
	if mime == "" {
		mime = "application/json"
	}

	// Iterate in template var order for stable generated output.
	boundList := make([]resourceBinding, 0, len(bindings))
	for _, name := range varNames {
		path := bindings[name]
		assign, err := messageFieldAssignStringPath(g, m.Input, path, "in")
		if err != nil {
			return resourceReadTemplateData{}, fmt.Errorf("%s.%s: binding %q: %w",
				svc.GoName, m.GoName, path, err)
		}
		boundList = append(boundList, resourceBinding{
			Placeholder: name,
			FieldAssign: assign,
		})
	}

	unique := svc.GoName + "_" + m.GoName

	return resourceReadTemplateData{
		URITemplate:     uriTemplate,
		URITemplateVar:  "_" + unique + "_URITemplate",
		Name:            ro.GetNameField(),
		Description:     ro.GetDescriptionField(),
		MIMEType:        mime,
		BlobFieldGetter: blobGetter,
		Bindings:        boundList,
		InputTypeRef:    g.QualifiedGoIdent(m.Input.GoIdent),
		OutputTypeRef:   g.QualifiedGoIdent(m.Output.GoIdent),
		ClientMethod:    "client." + m.GoName,
		UniqueName:      unique,
	}, nil
}

// buildResourceListTemplateData is the analog for `resource_list`.
func buildResourceListTemplateData(
	g *protogen.GeneratedFile,
	svc *protogen.Service,
	m *protogen.Method,
	rlo *protomcpv1.ResourceListOptions,
) (resourceListTemplateData, error) {
	if m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer() {
		return resourceListTemplateData{}, fmt.Errorf(
			"%s.%s: protomcp.v1.resource_list requires a unary RPC",
			svc.GoName, m.GoName,
		)
	}

	itemPath := rlo.GetItemPath()
	if itemPath == "" {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: resource_list.item_path is empty",
			svc.GoName, m.GoName)
	}
	itemMD, err := schema.ResolveItemMessage(m.Output.Desc, itemPath)
	if err != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}
	itemGetter, err := repeatedFieldGetter(m.Output.Desc, itemPath, "resp")
	if err != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: item_path: %w", svc.GoName, m.GoName, err)
	}

	uriTemplate := rlo.GetUriTemplate()
	if uriTemplate == "" {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: resource_list.uri_template is empty",
			svc.GoName, m.GoName)
	}
	_, varNames, err := schema.ParseURITemplate(uriTemplate)
	if err != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, err)
	}
	bindings := bindingMap(rlo.GetUriBindings())
	if bErr := schema.ValidatePlaceholderBindings(varNames, bindings, itemMD); bErr != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: %w", svc.GoName, m.GoName, bErr)
	}

	if nf := rlo.GetNameField(); nf == "" {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: resource_list.name_field is required",
			svc.GoName, m.GoName)
	}
	nameTmpl, err := schema.ParseMustache(rlo.GetNameField())
	if err != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: name_field: %w", svc.GoName, m.GoName, err)
	}
	if err := schema.ValidateMustacheFieldPaths(nameTmpl, itemMD); err != nil {
		return resourceListTemplateData{}, fmt.Errorf("%s.%s: name_field: %w", svc.GoName, m.GoName, err)
	}
	if df := rlo.GetDescriptionField(); df != "" {
		dTmpl, err := schema.ParseMustache(df)
		if err != nil {
			return resourceListTemplateData{}, fmt.Errorf("%s.%s: description_field: %w", svc.GoName, m.GoName, err)
		}
		if err := schema.ValidateMustacheFieldPaths(dTmpl, itemMD); err != nil {
			return resourceListTemplateData{}, fmt.Errorf("%s.%s: description_field: %w", svc.GoName, m.GoName, err)
		}
	}

	mime := rlo.GetMimeType()
	if mime == "" {
		mime = "application/json"
	}

	boundList := make([]resourceListBinding, 0, len(bindings))
	for _, name := range varNames {
		path := bindings[name]
		getter, err := messageFieldGetterForString(itemMD, path, "item")
		if err != nil {
			return resourceListTemplateData{}, fmt.Errorf("%s.%s: binding %q: %w",
				svc.GoName, m.GoName, path, err)
		}
		boundList = append(boundList, resourceListBinding{
			Placeholder: name,
			FieldGetter: getter,
		})
	}

	unique := svc.GoName + "_" + m.GoName + "_List"
	return resourceListTemplateData{
		URITemplate:     uriTemplate,
		URITemplateVar:  "_" + unique + "_URITemplate",
		Name:            rlo.GetNameField(),
		Description:     rlo.GetDescriptionField(),
		MIMEType:        mime,
		ItemFieldGetter: itemGetter,
		ItemBindings:    boundList,
		InputTypeRef:    g.QualifiedGoIdent(m.Input.GoIdent),
		OutputTypeRef:   g.QualifiedGoIdent(m.Output.GoIdent),
		ClientMethod:    "client." + m.GoName,
		UniqueName:      unique,
	}, nil
}

// messageFieldAssignStringPath returns a Go statement assigning `val`
// to the field at path on `root`. Nested messages are lazily allocated;
// the terminal must be a scalar string field.
func messageFieldAssignStringPath(g *protogen.GeneratedFile, start *protogen.Message, path, root string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty field path")
	}
	segments := strings.Split(path, ".")
	cur := start
	var b strings.Builder
	goExpr := root
	for i, seg := range segments {
		f := findProtogenField(cur, seg)
		if f == nil {
			return "", fmt.Errorf("field %q not found on %s", seg, cur.Desc.FullName())
		}
		fd := f.Desc
		goField := f.GoName
		isLast := i == len(segments)-1
		if isLast {
			if fd.Kind() != protoreflect.StringKind {
				return "", fmt.Errorf("terminal field %q on %s is not a string (kind=%s)",
					seg, cur.Desc.FullName(), fd.Kind())
			}
			fmt.Fprintf(&b, "%s.%s = val\n", goExpr, goField)
			return b.String(), nil
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return "", fmt.Errorf("field %q on %s: cannot descend (kind=%s, repeated=%t)",
				seg, cur.Desc.FullName(), fd.Kind(), fd.IsList())
		}
		nested := f.Message
		if nested == nil {
			return "", fmt.Errorf("field %q on %s: nested message descriptor missing from protogen tree",
				seg, cur.Desc.FullName())
		}
		nestedQualified := g.QualifiedGoIdent(nested.GoIdent)
		fmt.Fprintf(&b, "if %s.%s == nil { %s.%s = &%s{} }\n",
			goExpr, goField, goExpr, goField, nestedQualified)
		goExpr = goExpr + "." + goField
		cur = nested
	}
	return b.String(), nil
}

// findProtogenField matches name against m's fields by JSON name or
// raw proto name.
func findProtogenField(m *protogen.Message, name string) *protogen.Field {
	for _, f := range m.Fields {
		fd := f.Desc
		if string(fd.Name()) == name || fd.JSONName() == name {
			return f
		}
	}
	return nil
}

// messageFieldGetter returns a Go expression of the form
// `root.GetX().GetY()` that evaluates to the terminal field's value.
func messageFieldGetter(md protoreflect.MessageDescriptor, path, root string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty field path")
	}
	segments := strings.Split(path, ".")
	cur := md
	expr := root
	for i, seg := range segments {
		fd := findProtoField(cur, seg)
		if fd == nil {
			return "", fmt.Errorf("field %q not found on %s", seg, cur.FullName())
		}
		expr = expr + ".Get" + protoGoName(fd) + "()"
		isLast := i == len(segments)-1
		if isLast {
			return expr, nil
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return "", fmt.Errorf("field %q on %s: cannot descend", seg, cur.FullName())
		}
		cur = fd.Message()
	}
	return expr, nil
}

// messageFieldGetterForString rejects non-string terminals.
func messageFieldGetterForString(md protoreflect.MessageDescriptor, path, root string) (string, error) {
	fd := schema.FindMessageField(md, path)
	if fd == nil {
		return "", fmt.Errorf("field %q not found on %s", path, md.FullName())
	}
	if fd.Kind() != protoreflect.StringKind {
		return "", fmt.Errorf("terminal field %q on %s must be string (kind=%s)",
			path, md.FullName(), fd.Kind())
	}
	return messageFieldGetter(md, path, root)
}

// repeatedFieldGetter returns a Go expression evaluating to the
// []*Item slice for the repeated terminal field at path.
func repeatedFieldGetter(md protoreflect.MessageDescriptor, path, root string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty field path")
	}
	segments := strings.Split(path, ".")
	cur := md
	expr := root
	for i, seg := range segments {
		fd := findProtoField(cur, seg)
		if fd == nil {
			return "", fmt.Errorf("field %q not found on %s", seg, cur.FullName())
		}
		expr = expr + ".Get" + protoGoName(fd) + "()"
		isLast := i == len(segments)-1
		if isLast {
			if !fd.IsList() || fd.Kind() != protoreflect.MessageKind {
				return "", fmt.Errorf("item_path terminal %q on %s must be a repeated message",
					seg, cur.FullName())
			}
			return expr, nil
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return "", fmt.Errorf("item_path segment %q on %s: cannot descend",
				seg, cur.FullName())
		}
		cur = fd.Message()
	}
	return expr, nil
}

// findProtoField matches name against fields by JSONName first, then
// raw proto name.
func findProtoField(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := md.Fields()
	if fd := fields.ByJSONName(name); fd != nil {
		return fd
	}
	return fields.ByName(protoreflect.Name(name))
}

// protoGoName returns the Go-identifier form of a proto field name
// (snake_case → CamelCase), matching protogen's convention.
func protoGoName(fd protoreflect.FieldDescriptor) string {
	return snakeToCamel(string(fd.Name()))
}

func snakeToCamel(s string) string {
	var b strings.Builder
	capNext := true
	for _, r := range s {
		switch {
		case r == '_':
			capNext = true
		case capNext:
			if r >= 'a' && r <= 'z' {
				r = r - 'a' + 'A'
			}
			b.WriteRune(r)
			capNext = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
