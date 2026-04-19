package gen

import (
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
)

// methodDescription returns the human-readable description to surface as the
// MCP tool description for method m. If the method carries a protomcp.v1.tool
// description override, that value is returned verbatim (with its whitespace
// preserved); otherwise the leading proto comment is extracted, cleaned of
// tool-specific directives (buf:lint:, @ignore-comment) via schema.CleanComment,
// and returned. An empty string signals "no description" to the template.
func methodDescription(m *protogen.Method, override string) string {
	if override != "" {
		return strings.TrimSpace(override)
	}
	leading := string(m.Comments.Leading)
	if leading == "" {
		return ""
	}
	return strings.TrimSpace(schema.CleanComment(leading))
}
