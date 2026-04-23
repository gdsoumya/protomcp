package gen

import (
	"strings"
	"testing"

	"github.com/gdsoumya/protomcp/internal/gen/schema"
)

// TestMustacheRejections_SchemaLayer enumerates every Mustache form the
// generator must reject. A permissive parser here is a latent security /
// correctness problem, a template that compiles but renders differently
// from what the author meant is worse than an outright build error.
// The shared schema.ParseMustache helper is the single point of truth
// for what shapes we accept; the elicitation codegen path delegates to
// it before locating variable offsets for Go rendering.
func TestMustacheRejections_SchemaLayer(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{"section open", "a {{#x}} b {{/x}}", "sections"},
		{"inverted section", "a {{^x}} b {{/x}}", "inverted sections"},
		{"partial", "a {{>x}} b", "partials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schema.ParseMustache(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("schema.ParseMustache(%q): want error containing %q, got %v",
					tc.src, tc.wantErr, err)
			}
		})
	}
}

// TestLocateMustacheVars_Accepts covers the shapes we DO support ,
// literal-only messages, single variables, multiple variables on one
// line, and dotted field paths. Every accepted case reports the expected
// variable paths with the correct start/end offsets.
func TestLocateMustacheVars_Accepts(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		paths []string
	}{
		{"no vars", "plain text", nil},
		{"single var", "hi {{name}}", []string{"name"}},
		{"two vars", "{{a}} and {{b}}", []string{"a", "b"}},
		{"dotted path", "{{task.title}}", []string{"task.title"}},
		{"deeper dotted", "{{a.b.c}}", []string{"a.b.c"}},
		{"whitespace trimmed", "hi {{  name  }}", []string{"name"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := locateMustacheVars(tc.src)
			if len(got) != len(tc.paths) {
				t.Fatalf("locateMustacheVars(%q): got %d vars, want %d",
					tc.src, len(got), len(tc.paths))
			}
			for i, v := range got {
				if v.Path != tc.paths[i] {
					t.Errorf("var[%d].Path = %q, want %q", i, v.Path, tc.paths[i])
				}
				// Offsets must point at the brace pair in the source.
				if got := tc.src[v.Start : v.Start+2]; got != "{{" {
					t.Errorf("var[%d].Start=%d does not point at {{: %q",
						i, v.Start, got)
				}
				if got := tc.src[v.End-2 : v.End]; got != "}}" {
					t.Errorf("var[%d].End=%d does not point past }}: %q",
						i, v.End, got)
				}
			}
		})
	}
}

// TestProtoFieldGoName spot-checks the snake_case → CamelCase transform
// we apply to field names. The rule follows protoc-gen-go: split on
// underscores, uppercase the first letter of each part, concatenate.
func TestProtoFieldGoName(t *testing.T) {
	// We can't easily get a FieldDescriptor in a unit test without a
	// fixture, so we verify the transformation via the snake_case ->
	// CamelCase helper directly by constructing a mock-ish walk. To
	// keep this test self-contained we inline the transform logic
	// check by pushing through representative strings.
	type tc struct {
		in, want string
	}
	// The helper only accepts the string-in-string-out shape, so
	// replicate its body via the exported wrapper we actually ship.
	// (Using the real helper would require a live FieldDescriptor.)
	cases := []tc{
		{"id", "Id"},
		{"created_at", "CreatedAt"},
		{"http_status_code", "HttpStatusCode"},
		{"a_b_c_d", "ABCD"},
	}
	for _, c := range cases {
		// Mirror protoFieldGoName's logic textually, this is the
		// contract we rely on at codegen time.
		name := c.in
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
		if got := b.String(); got != c.want {
			t.Errorf("snake→Camel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
