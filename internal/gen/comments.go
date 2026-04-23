package gen

import (
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
)

// commentPragmaPrefixes are whole-line markers stripped from proto
// leading/trailing comments before surfacing as MCP descriptions.
// Matched case-sensitively against the trimmed line; mid-line
// occurrences are preserved.
//
// Supported pragmas:
//   - buf:lint:ignore RULE_NAME (+ friends): buf-lint instructions.
//   - @ignore-comment ...: MCP-for-proto convention for hiding lines
//     from LLM-visible text.
//   - @exclude ...: protoc-gen-doc alias for @ignore-comment.
var commentPragmaPrefixes = []string{
	"buf:lint:",
	"@ignore-comment",
	"@exclude",
}

// StripCommentPragmas removes tool-directive lines (whole-line
// matches against commentPragmaPrefixes) from a raw proto comment and
// returns the rest unchanged. Line ordering and inter-line whitespace
// on non-pragma lines are preserved.
func StripCommentPragmas(comment string) string {
	if comment == "" {
		return ""
	}
	lines := strings.Split(comment, "\n")
	out := lines[:0]
outer:
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		for _, p := range commentPragmaPrefixes {
			if strings.HasPrefix(trimmed, p) {
				continue outer
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// methodDescription returns the MCP tool description for m. An
// explicit override wins; otherwise the leading proto comment is
// cleaned of pragmas and returned.
func methodDescription(m *protogen.Method, override string) string {
	if override != "" {
		return strings.TrimSpace(override)
	}
	leading := string(m.Comments.Leading)
	if leading == "" {
		return ""
	}
	cleaned := StripCommentPragmas(leading)
	return strings.TrimSpace(schema.CleanComment(cleaned))
}
