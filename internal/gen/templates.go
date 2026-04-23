package gen

import (
	"embed"
	"strings"
	"text/template"
)

// templateFS holds the generator's Go source templates.
//
//go:embed templates/*.go.tmpl
var templateFS embed.FS

// parsedTemplates is the compiled template set, registered by file
// basename.
var parsedTemplates = template.Must(
	template.New("protomcp").
		Funcs(templateFuncs).
		ParseFS(templateFS, "templates/*.go.tmpl"),
)

// resourceWithFile bundles a per-resource template-data struct with
// the enclosing fileTemplateData so sub-templates can reach both.
type resourceWithFile struct {
	R any // resourceReadTemplateData | resourceListTemplateData
	F *fileTemplateData
}

var templateFuncs = template.FuncMap{
	// commentBlock prefixes non-empty lines with "// " and blank
	// lines with "//" to match gofmt's doc-comment style.
	"commentBlock": func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		lines := strings.Split(s, "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimRight(line, " \t")
			if trimmed == "" {
				out = append(out, "//")
				continue
			}
			out = append(out, "// "+trimmed)
		}
		return strings.Join(out, "\n")
	},
	// safeRawString emits a Go expression for s, preferring raw-string
	// literals and stitching embedded backticks via concatenation.
	"safeRawString": safeRawString,
	// withFile pairs a per-resource struct with the enclosing file
	// template data (exposed as .R and .F).
	"withFile": func(r any, f *fileTemplateData) resourceWithFile {
		return resourceWithFile{R: r, F: f}
	},
}
