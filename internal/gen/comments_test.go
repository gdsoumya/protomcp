package gen

import (
	"strings"
	"testing"
)

// TestStripCommentPragmas_FiltersLines exercises the pragma-stripping
// helper in isolation. Lines that are whole-line lint directives or
// @ignore-comment markers must be dropped; every other line (including
// those that only *contain* a pragma as a substring) must be preserved
// verbatim. Leading / trailing whitespace on pragma lines is ignored so
// an indented marker still gets stripped.
func TestStripCommentPragmas_FiltersLines(t *testing.T) {
	in := strings.Join([]string{
		"Task is the canonical task resource.",
		"buf:lint:ignore FIELD_LOWER_SNAKE_CASE",
		"  buf:lint:ignore ANOTHER",
		"@ignore-comment internal note: should not leak.",
		"Used across the API.",
		"This mentions buf:lint: in passing; must be kept.",
		"",
	}, "\n")
	want := strings.Join([]string{
		"Task is the canonical task resource.",
		"Used across the API.",
		"This mentions buf:lint: in passing; must be kept.",
		"",
	}, "\n")
	got := StripCommentPragmas(in)
	if got != want {
		t.Errorf("StripCommentPragmas mismatch\nwant: %q\ngot:  %q", want, got)
	}
}

// TestStripCommentPragmas_Empty ensures the helper is safe on the empty
// input, a common case because unannotated messages have no comment at
// all and the caller does not pre-guard.
func TestStripCommentPragmas_Empty(t *testing.T) {
	if got := StripCommentPragmas(""); got != "" {
		t.Errorf("StripCommentPragmas(\"\") = %q, want empty", got)
	}
}

// TestStripCommentPragmas_OnlyPragmas ensures that a comment consisting
// entirely of pragma lines reduces to the empty string, not to a string
// of blank separators, so the caller's downstream TrimSpace produces
// the expected "no description".
func TestStripCommentPragmas_OnlyPragmas(t *testing.T) {
	in := "buf:lint:ignore A\n@ignore-comment B\n@exclude C\n"
	if got := strings.TrimSpace(StripCommentPragmas(in)); got != "" {
		t.Errorf("StripCommentPragmas all-pragma = %q, want empty after trim", got)
	}
}

// TestStripCommentPragmas_ExcludeAlias verifies that @exclude (the
// protoc-gen-doc convention) is accepted as an alias for
// @ignore-comment so proto files migrating from that tool do not have
// to rewrite their pragmas.
func TestStripCommentPragmas_ExcludeAlias(t *testing.T) {
	in := strings.Join([]string{
		"Public description.",
		"@exclude this is an internal note.",
		"@exclude another one",
		"More public prose.",
	}, "\n")
	want := strings.Join([]string{
		"Public description.",
		"More public prose.",
	}, "\n")
	got := StripCommentPragmas(in)
	if got != want {
		t.Errorf("mismatch\nwant: %q\ngot:  %q", want, got)
	}
}

// TestGenerate_PragmasFixture_SchemaDescription verifies that a field's
// leading comment with a pragma line embedded produces a JSON Schema
// description that excludes the pragma but keeps the surrounding prose.
// This is the end-to-end proof that StripCommentPragmas wired into the
// comments path reaches the schema generation path as well (the schema
// package's own CleanComment handles field-level comments; we keep both
// hooks because the generator description pipeline also feeds the tool
// description, and we do not want the two paths to diverge).
func TestGenerate_PragmasFixture_SchemaDescription(t *testing.T) {
	out := runGenerate(t, "pragmas.proto")

	// Drop into the input-schema string literal for RunTaskRequest and
	// parse back the JSON so we can read its field-level description
	// verbatim. We do not hardcode the whole schema JSON because
	// deterministic but not-quite-stable field ordering would make the
	// assertion needlessly brittle.
	const marker = "_Pragmas_RunTask_InputSchema = protomcp.MustParseSchema("
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("input schema marker %q not found in generated file:\n%s", marker, out)
	}
	tail := out[idx+len(marker):]
	// The emitted expression may start with either `...` (raw-string) or
	// "..." (strconv.Quote fallback). We walk to the matching end so the
	// assertion survives either encoding.
	if len(tail) == 0 {
		t.Fatalf("schema literal truncated")
	}
	// Expect the backtick-bearing prose "Use `grpcurl` to test." is NOT
	// present in the pragma comments we embedded for this message (the
	// backtick test lives on the method's leading comment). But the real
	// line "The task id to run." should be there, and the pragma must NOT.
	schemaLiteral := extractLiteral(tail)
	if !strings.Contains(schemaLiteral, "The task id to run.") {
		t.Errorf("expected id field description prose in schema literal; got:\n%s", schemaLiteral)
	}
	if strings.Contains(schemaLiteral, "buf:lint:") {
		t.Errorf("schema literal must not include buf:lint: pragma:\n%s", schemaLiteral)
	}
}

// extractLiteral reads the leading Go string-concat expression from s
// (either raw-string or double-quoted, possibly chained with " + ") and
// returns the concatenated payload. The helper stops at the first closing
// paren at nesting depth 0 so the resulting slice ends at the end of the
// literal expression, never past it.
func extractLiteral(s string) string {
	// We don't need strict tokenization, just enough to collect the
	// payload of a chain of `...` / "..." segments separated by " + ".
	var b strings.Builder
	i, n := 0, len(s)
	for i < n {
		switch s[i] {
		case '`':
			j := i + 1
			for j < n && s[j] != '`' {
				j++
			}
			if j >= n {
				return b.String()
			}
			b.WriteString(s[i+1 : j])
			i = j + 1
		case '"':
			j := i + 1
			for j < n && s[j] != '"' {
				if s[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				j++
			}
			if j >= n {
				return b.String()
			}
			b.WriteString(s[i+1 : j])
			i = j + 1
		case ' ', '+', '\t':
			i++
		default:
			return b.String()
		}
	}
	return b.String()
}

// TestGenerate_PragmasFixture drives the generator against the pragmas.proto
// fixture, which carries both a backtick in its leading comment and explicit
// pragma lines. The test asserts:
//   - generation succeeds (proves safeRawString keeps the raw-string literal
//     valid even when the comment contains a backtick),
//   - the generated file contains the RunTask tool registration,
//   - the pragma lines do NOT leak into the generated Description string,
//   - the backtick-bearing prose ("Use `grpcurl` to test.") survives.
func TestGenerate_PragmasFixture(t *testing.T) {
	out := runGenerate(t, "pragmas.proto")

	cases := []substringCase{
		{"register function present", true, "RegisterPragmasMCPTools"},
		{"RunTask tool present", true, `"Pragmas_RunTask"`},
		{"pragma line not in description", false, "buf:lint:ignore"},
		{"ignore-comment not in description", false, "@ignore-comment"},
		// The real prose with the backtick should survive. We look for the
		// portion past the backtick so we don't have to anticipate how the
		// generator escaped the backtick in the emitted string literal.
		{"backtick prose survives", true, "grpcurl"},
	}
	assertSubstrings(t, out, cases)

	// Belt-and-suspenders: the emitted Go source must parse cleanly. If any
	// pragma-stripping or backtick-handling step left the file malformed,
	// validateGeneratedGo would already have caught it, but the top-level
	// Generate path only runs that check when writing through protogen, so
	// we re-parse the captured string here for an extra seatbelt.
	if err := validateGeneratedGo("pragmas.proto", "pragmas.mcp.pb.go", out); err != nil {
		t.Fatalf("generated output from pragmas.proto does not parse: %v", err)
	}
}
