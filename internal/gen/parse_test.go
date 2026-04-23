package gen

import (
	"strings"
	"testing"
)

// TestValidateGeneratedGo_Ok confirms the validator accepts the simplest
// well-formed Go source. The generator relies on the zero-error path so
// this is the regression anchor: if parsing of a trivial file ever starts
// failing, every run of the plugin will fail.
func TestValidateGeneratedGo_Ok(t *testing.T) {
	src := "package foo\n\nfunc Bar() {}\n"
	if err := validateGeneratedGo("foo.proto", "foo.mcp.pb.go", src); err != nil {
		t.Fatalf("validateGeneratedGo on valid source returned error: %v", err)
	}
}

// TestValidateGeneratedGo_Broken exercises the failure path. The broken
// source (dangling closing brace) should surface a codegen error whose
// message mentions both the proto filename and a non-zero line number ,
// those are the two hooks the user needs to locate the template defect.
func TestValidateGeneratedGo_Broken(t *testing.T) {
	// Use raw syntax that a scanner can tokenize but the parser rejects at
	// a specific line, so we get a precise line number in the error.
	src := "package foo\n\nfunc Bar() {\n\treturn 1 2 3\n}\n"
	err := validateGeneratedGo("myservice.proto", "myservice.mcp.pb.go", src)
	if err == nil {
		t.Fatal("validateGeneratedGo on broken source returned nil; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "myservice.proto") {
		t.Errorf("error should mention the proto filename; got: %s", msg)
	}
	if !strings.Contains(msg, "myservice.mcp.pb.go") {
		t.Errorf("error should mention the target Go filename; got: %s", msg)
	}
	// The excerpt heading is "--- generated source around line N ---" and
	// N must be > 0 for the error to be actionable.
	idx := strings.Index(msg, "around line ")
	if idx < 0 {
		t.Fatalf("error should include 'around line N' marker; got: %s", msg)
	}
	rest := msg[idx+len("around line "):]
	// Grab digits up to the first non-digit.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		t.Fatalf("error line number missing after 'around line '; got: %s", msg)
	}
	if rest[:end] == "0" {
		t.Errorf("error line number should be non-zero; got 0 in: %s", msg)
	}
}

// TestValidateGeneratedGo_BrokenComment covers the specific bug class the
// safeRawString helper exists to prevent: a proto comment containing a
// backtick ends up inside a Go raw-string literal in the generated file,
// which silently produces un-parseable Go. The validator must reject it
// with a pointer to the failure line.
func TestValidateGeneratedGo_BrokenComment(t *testing.T) {
	// package line on 1, var on 3 with an unterminated raw string on 3.
	src := "package foo\n\nvar x = `open but no close\n"
	err := validateGeneratedGo("tool.proto", "tool.mcp.pb.go", src)
	if err == nil {
		t.Fatal("want parse error for unterminated raw string, got nil")
	}
	if !strings.Contains(err.Error(), "tool.proto") {
		t.Errorf("missing proto filename in error: %v", err)
	}
}

// TestSafeRawString_NoBacktick is the common case: the string contains no
// backticks, so the helper returns a single raw-string literal that
// evaluates back to the input verbatim.
func TestSafeRawString_NoBacktick(t *testing.T) {
	cases := []string{
		"",
		"simple",
		"multi\nline",
		"quoted \"double\" and escaped \\n",
	}
	for _, s := range cases {
		got := safeRawString(s)
		assertEvalsTo(t, got, s)
	}
}

// TestSafeRawString_WithBacktick exercises the chunking path: backticks
// must be emitted in double-quoted segments and concatenated back in,
// yielding Go source that evaluates to exactly the original string.
func TestSafeRawString_WithBacktick(t *testing.T) {
	cases := []string{
		"`",
		"``",
		"a`b",
		"`leading",
		"trailing`",
		"use `grpcurl` to test",
		"``triple``",
		"mix `one` and ``two`` and plain",
	}
	for _, s := range cases {
		got := safeRawString(s)
		assertEvalsTo(t, got, s)
	}
}

// TestSafeRawString_Empty pins the special case: safeRawString("") must
// return something Go-parseable (not an empty string); an empty raw-string
// literal is the natural choice.
func TestSafeRawString_Empty(t *testing.T) {
	got := safeRawString("")
	if got != "``" {
		t.Errorf("safeRawString(\"\") = %q, want ``", got)
	}
}

// TestSafeRawString_GeneratedOutputParses wraps a backtick-bearing string
// in a full var declaration and feeds it to validateGeneratedGo. Without
// the helper, this would be an unterminated raw-string literal; with it,
// the resulting Go must parse cleanly. This is the integration guarantee
// between 1.1 (parse validation) and 1.2 (safe backtick handling).
func TestSafeRawString_GeneratedOutputParses(t *testing.T) {
	input := "use `grpcurl` to test"
	src := "package p\n\nvar x = " + safeRawString(input) + "\n"
	if err := validateGeneratedGo("x.proto", "x.mcp.pb.go", src); err != nil {
		t.Errorf("safeRawString output does not parse: %v\nsrc:\n%s", err, src)
	}
}

// assertEvalsTo compiles expr as a standalone Go variable initializer and
// asserts the literal is a well-formed expression. Full constant evaluation
// is overkill for this helper, what we need is confidence that the emitted
// fragment parses. The value round-trip is checked by building the string
// back manually using the same chunk rules the helper follows.
func assertEvalsTo(t *testing.T, expr, want string) {
	t.Helper()
	src := "package p\n\nvar x = " + expr + "\n"
	if err := validateGeneratedGo("t.proto", "t.mcp.pb.go", src); err != nil {
		t.Errorf("safeRawString(%q) = %q does not parse as Go: %v", want, expr, err)
		return
	}
	// Decode the expression by running the inverse: strip backticks and
	// concatenation joins, collect double-quoted backtick runs verbatim.
	// This is a lightweight sanity check that the helper did not lose or
	// insert characters; a full parser would be nicer but adds no value
	// over the parse-validation check above.
	got, ok := decodeSafeRawString(expr)
	if !ok {
		t.Errorf("safeRawString(%q) produced %q which does not match the expected chunk shape", want, expr)
		return
	}
	if got != want {
		t.Errorf("safeRawString(%q) round-trip = %q, want %q (expr=%s)", want, got, want, expr)
	}
}

// decodeSafeRawString reverses the safeRawString encoding: it consumes
// raw-string and double-quoted chunks separated by " + " joins, and
// returns the concatenated payload. Returns ok=false if the expression
// does not follow the expected shape.
func decodeSafeRawString(expr string) (string, bool) {
	parts := splitJoin(expr)
	var b strings.Builder
	for _, p := range parts {
		switch {
		case strings.HasPrefix(p, "`") && strings.HasSuffix(p, "`") && len(p) >= 2:
			b.WriteString(p[1 : len(p)-1])
		case strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"") && len(p) >= 2:
			b.WriteString(p[1 : len(p)-1])
		default:
			return "", false
		}
	}
	return b.String(), true
}

// splitJoin splits "a + b + c" at top-level " + " occurrences. The helper
// does not emit nested quotes or escapes so a simple substring split is
// sufficient.
func splitJoin(s string) []string {
	if !strings.Contains(s, " + ") {
		return []string{s}
	}
	return strings.Split(s, " + ")
}
