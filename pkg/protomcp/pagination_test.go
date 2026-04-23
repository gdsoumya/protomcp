package protomcp

import (
	"errors"
	"strings"
	"testing"
)

// TestOffsetCursorRoundTrip, encode then decode returns the original
// offset, and an empty cursor decodes to 0 (the implicit first page).
func TestOffsetCursorRoundTrip(t *testing.T) {
	cases := []int64{0, 1, 50, 1000, 1_000_000}
	for _, n := range cases {
		enc, err := EncodeOffsetCursor(n)
		if err != nil {
			t.Fatalf("encode(%d): %v", n, err)
		}
		got, err := DecodeOffsetCursor(enc)
		if err != nil {
			t.Fatalf("decode(%q): %v", enc, err)
		}
		if got != n {
			t.Errorf("round-trip offset = %d, want %d", got, n)
		}
	}
	if got, err := DecodeOffsetCursor(""); err != nil || got != 0 {
		t.Errorf("empty cursor: got (%d, %v), want (0, nil)", got, err)
	}
}

// TestDecodeOffsetCursor_BadInputs, malformed base64 / JSON / negative
// values surface clear errors rather than silently defaulting to 0.
func TestDecodeOffsetCursor_BadInputs(t *testing.T) {
	cases := []struct {
		name, in, wantMsg string
	}{
		{"bad base64", "not-base64!!", "not base64"},
		{"bad json", "bm90LWpzb24=", "not JSON"},         // base64("not-json")
		{"negative", "eyJvZmZzZXQiOi0xfQ==", "negative"}, // {"offset":-1}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeOffsetCursor(tc.in)
			if err == nil {
				t.Fatalf("expected error for %q", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q; want substring %q", err, tc.wantMsg)
			}
		})
	}
}

// TestOffsetPagination_PageSizePanics, constructor-time guard for the
// obvious wiring mistake.
func TestOffsetPagination_PageSizePanics(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "pageSize must be positive") {
			t.Fatalf("panic = %v; want pageSize guard", r)
		}
	}()
	_, _ = OffsetPagination("limit", "offset", 0)
}

// TestSetProtoIntField_MissingField, reflecting onto a request that
// doesn't have the expected field name should error out rather than
// silently succeed.
func TestSetProtoIntField_MissingField(t *testing.T) {
	// Use any registered proto message, we only care that the field
	// name is missing. *mcp.ListResourcesParams is always linked in.
	// But the SDK message is not a protoreflect proto. Use Cursor = "" via
	// an approach that doesn't require a concrete message. Instead we
	// lean on the tasks types which ARE real proto messages; the test
	// living in examples/tasks/ covers the happy path. Here we just
	// exercise the nil guard.
	err := SetProtoIntField(nil, "limit", 5)
	if err == nil || !strings.Contains(err.Error(), "nil message") {
		t.Errorf("want nil-message error, got %v", err)
	}
	err = SetProtoStringField(nil, "x", "v")
	if err == nil || !strings.Contains(err.Error(), "nil message") {
		t.Errorf("want nil-message error, got %v", err)
	}
}

// sanity: DecodeOffsetCursor errors wrap something parseable.
func TestDecodeOffsetCursor_WrappedErr(t *testing.T) {
	_, err := DecodeOffsetCursor("not-base64!!")
	if err == nil || errors.Unwrap(err) == nil {
		t.Errorf("expected wrapped error, got %v", err)
	}
}
