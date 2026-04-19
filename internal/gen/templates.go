package gen

import (
	"embed"
	"strings"
	"text/template"
)

// templateFS holds the generator's Go source templates. Keeping them as
// separate files (rather than raw string literals in code) makes them
// easier to read, lint, and diff.
//
//go:embed templates/*.go.tmpl
var templateFS embed.FS

// parsedTemplates is the compiled template set used by the generator.
// Each template is registered under its file basename (e.g. "file.go.tmpl",
// "tool.go.tmpl"); the generator invokes them by name.
var parsedTemplates = template.Must(
	template.New("protomcp").
		Funcs(templateFuncs).
		ParseFS(templateFS, "templates/*.go.tmpl"),
)

// templateFuncs is the FuncMap exposed to every template in the set.
// It is intentionally small — every helper here is a trivial formatter;
// any non-trivial logic belongs in the Go code that builds the template
// context, not in the template itself.
var templateFuncs = template.FuncMap{
	// commentBlock prefixes every non-empty line with "// " so multi-line
	// proto comments survive as valid Go doc comments in the generated file.
	// Blank lines become "//" (no trailing space) to match gofmt's style.
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
}
