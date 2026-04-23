package schema

import (
	"fmt"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/yosida95/uritemplate/v3"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ParseMustache parses a Mustache template. Sections, inverted
// sections, and partials are rejected so templates stay logic-less.
func ParseMustache(src string) (*mustache.Template, error) {
	tmpl, err := mustache.ParseString(src)
	if err != nil {
		return nil, err
	}
	for _, tag := range tmpl.Tags() {
		switch tag.Type() {
		case mustache.Variable:
		case mustache.Section:
			return nil, fmt.Errorf("mustache: sections ({{#%s}}) are not supported; "+
				"use only plain variable interpolation", tag.Name())
		case mustache.InvertedSection:
			return nil, fmt.Errorf("mustache: inverted sections ({{^%s}}) are not supported; "+
				"use only plain variable interpolation", tag.Name())
		case mustache.Partial:
			return nil, fmt.Errorf("mustache: partials ({{>%s}}) are not supported", tag.Name())
		default:
			return nil, fmt.Errorf("mustache: tag %q has unsupported type %v", tag.Name(), tag.Type())
		}
	}
	return tmpl, nil
}

// MustacheVariables returns unique variable names in template order.
func MustacheVariables(tmpl *mustache.Template) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, tag := range tmpl.Tags() {
		if tag.Type() != mustache.Variable {
			continue
		}
		name := tag.Name()
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// ValidateMustacheFieldPaths verifies each variable resolves to a
// real field path on md. Dotted paths cross nested messages; segments
// are looked up by protojson JSON name.
func ValidateMustacheFieldPaths(tmpl *mustache.Template, md protoreflect.MessageDescriptor) error {
	for _, name := range MustacheVariables(tmpl) {
		if err := validateFieldPath(name, md); err != nil {
			return fmt.Errorf("mustache variable {{%s}}: %w", name, err)
		}
	}
	return nil
}

// ParseURITemplate parses an RFC 6570 URI template and returns the
// compiled Template plus its placeholder names.
func ParseURITemplate(src string) (*uritemplate.Template, []string, error) {
	t, err := uritemplate.New(src)
	if err != nil {
		return nil, nil, fmt.Errorf("URI template %q: %w", src, err)
	}
	return t, t.Varnames(), nil
}

// ValidatePlaceholderBindings requires exactly one binding per
// placeholder in tmplVars, no extras, and every binding's field path
// to resolve on md.
func ValidatePlaceholderBindings(tmplVars []string, bindings map[string]string, md protoreflect.MessageDescriptor) error {
	tmplSet := make(map[string]struct{}, len(tmplVars))
	for _, v := range tmplVars {
		tmplSet[v] = struct{}{}
	}

	var missing []string
	for _, v := range tmplVars {
		if _, ok := bindings[v]; !ok {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("URI template placeholders missing bindings: %s",
			strings.Join(missing, ", "))
	}

	var extra []string
	for p := range bindings {
		if _, ok := tmplSet[p]; !ok {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("bindings reference placeholders not in URI template: %s",
			strings.Join(extra, ", "))
	}

	for p, path := range bindings {
		if path == "" {
			return fmt.Errorf("binding for placeholder %q: empty field path", p)
		}
		if err := validateFieldPath(path, md); err != nil {
			return fmt.Errorf("binding %q -> %q: %w", p, path, err)
		}
	}
	return nil
}

// ResolveItemMessage walks itemPath on md and returns the element
// message descriptor. The terminal segment must be a repeated message
// field (resources are object-shaped in MCP).
func ResolveItemMessage(md protoreflect.MessageDescriptor, itemPath string) (protoreflect.MessageDescriptor, error) {
	if itemPath == "" {
		return nil, fmt.Errorf("item_path is empty")
	}
	segments := strings.Split(itemPath, ".")
	cur := md
	for i, seg := range segments {
		fd := findField(cur, seg)
		if fd == nil {
			return nil, fmt.Errorf("item_path segment %q not found on %s", seg, cur.FullName())
		}
		isLast := i == len(segments)-1
		if !isLast {
			if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
				return nil, fmt.Errorf("item_path segment %q on %s must be a singular message", seg, cur.FullName())
			}
			cur = fd.Message()
			continue
		}
		if !fd.IsList() {
			return nil, fmt.Errorf("item_path terminal segment %q on %s must be a repeated field", seg, cur.FullName())
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("item_path terminal segment %q on %s must be a repeated message", seg, cur.FullName())
		}
		return fd.Message(), nil
	}
	return nil, fmt.Errorf("item_path %q: internal walk inconsistency", itemPath)
}

// FindMessageField resolves a dotted path on md, matching segments by
// JSON name first, then proto name. Returns nil on any failure.
func FindMessageField(md protoreflect.MessageDescriptor, path string) protoreflect.FieldDescriptor {
	fd, err := resolveFieldPath(path, md)
	if err != nil {
		return nil
	}
	return fd
}

// validateFieldPath checks that path resolves on md.
func validateFieldPath(path string, md protoreflect.MessageDescriptor) error {
	_, err := resolveFieldPath(path, md)
	return err
}

func resolveFieldPath(path string, md protoreflect.MessageDescriptor) (protoreflect.FieldDescriptor, error) {
	if path == "" {
		return nil, fmt.Errorf("empty field path")
	}
	segments := strings.Split(path, ".")
	cur := md
	var last protoreflect.FieldDescriptor
	for i, seg := range segments {
		fd := findField(cur, seg)
		if fd == nil {
			return nil, fmt.Errorf("field %q not found on %s", seg, cur.FullName())
		}
		last = fd
		isLast := i == len(segments)-1
		if isLast {
			break
		}
		if fd.IsList() || fd.IsMap() {
			return nil, fmt.Errorf("field %q on %s is repeated/map; cannot descend further", seg, cur.FullName())
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("field %q on %s is not a message; cannot descend further", seg, cur.FullName())
		}
		cur = fd.Message()
	}
	return last, nil
}

// findField matches name by JSON name first, then raw proto name,
// mirroring protojson.Unmarshal.
func findField(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := md.Fields()
	if fd := fields.ByJSONName(name); fd != nil {
		return fd
	}
	return fields.ByName(protoreflect.Name(name))
}
