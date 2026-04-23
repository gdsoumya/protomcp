package gen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// mustacheVar is a single {{variable}} reference located by byte
// offsets. Start and End bound the entire tag including the "{{" and
// "}}" markers.
type mustacheVar struct {
	Path  string
	Start int
	End   int
}

// locateMustacheVars scans src and returns every {{name}} tag with
// byte offsets. Tag-kind validation is assumed to have run via
// schema.ParseMustache upstream.
func locateMustacheVars(src string) []mustacheVar {
	var out []mustacheVar
	for i := 0; i < len(src); {
		open := strings.Index(src[i:], "{{")
		if open < 0 {
			break
		}
		open += i
		if strings.HasPrefix(src[open:], "{{{") {
			i = open + 3
			continue
		}
		closeIdx := strings.Index(src[open+2:], "}}")
		if closeIdx < 0 {
			break
		}
		closeIdx += open + 2
		body := strings.TrimSpace(src[open+2 : closeIdx])
		if body == "" {
			i = closeIdx + 2
			continue
		}
		switch body[0] {
		case '#', '^', '/', '>', '!', '=', '&':
			i = closeIdx + 2
			continue
		}
		out = append(out, mustacheVar{Path: body, Start: open, End: closeIdx + 2})
		i = closeIdx + 2
	}
	return out
}

// findFieldByJSONName scans md.Fields for a field whose JSONName
// matches name.
func findFieldByJSONName(md protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.JSONName() == name {
			return fd
		}
	}
	return nil
}

// renderMustacheGoExpr emits a Go source expression rendering src at
// runtime: literal segments and fmt.Sprintf("%v", getter) joined with
// " + ". Getters are used (not field access) so nil messages produce
// zero values, matching Mustache's missing-variable semantics and
// avoiding nil derefs.
func renderMustacheGoExpr(src string, vars []mustacheVar, md protoreflect.MessageDescriptor, receiverExpr, qFmtSprintf string) (string, error) {
	if len(vars) == 0 {
		return goStringLiteral(src), nil
	}
	var parts []string
	cursor := 0
	for _, v := range vars {
		if v.Start > cursor {
			parts = append(parts, goStringLiteral(src[cursor:v.Start]))
		}
		getter, err := mustacheGoGetter(v.Path, md, receiverExpr)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s(\"%%v\", %s)", qFmtSprintf, getter))
		cursor = v.End
	}
	if cursor < len(src) {
		parts = append(parts, goStringLiteral(src[cursor:]))
	}
	return strings.Join(parts, " + "), nil
}

// mustacheGoGetter produces the Go expression reading path off
// receiverExpr as a chain of GetFoo() calls.
func mustacheGoGetter(path string, md protoreflect.MessageDescriptor, receiverExpr string) (string, error) {
	segs := strings.Split(path, ".")
	expr := receiverExpr
	cur := md
	for i, seg := range segs {
		fd := cur.Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			fd = findFieldByJSONName(cur, seg)
		}
		if fd == nil {
			return "", fmt.Errorf("segment %q does not match any field on message %s", seg, cur.FullName())
		}
		goName := protoFieldGoName(fd)
		expr = expr + ".Get" + goName + "()"
		if i == len(segs)-1 {
			return expr, nil
		}
		cur = fd.Message()
	}
	return expr, nil
}

// protoFieldGoName returns the exported Go identifier protoc-gen-go
// uses for fd (underscore-separated lowercase → CamelCase).
func protoFieldGoName(fd protoreflect.FieldDescriptor) string {
	name := string(fd.Name())
	var b strings.Builder
	upper := true
	for _, r := range name {
		if r == '_' {
			upper = true
			continue
		}
		if upper {
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			upper = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// goStringLiteral quotes s as a Go interpreted-string literal. Raw
// strings are avoided because input may contain backticks.
func goStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
