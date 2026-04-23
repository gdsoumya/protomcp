package protomcp

import "testing"

func TestSanitizeMetadataValue(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"clean ascii", "foo-bar_123", "foo-bar_123"},
		{"printable with tab", "a\tb", "a\tb"},
		{"strip CR", "foo\rbar", "foobar"},
		{"strip LF", "foo\nbar", "foobar"},
		{"strip CRLF", "foo\r\nx-admin: 1", "foox-admin: 1"},
		{"strip NUL", "foo\x00bar", "foobar"},
		{"strip all control except tab", "a\x01\x02\x03\tb", "a\tb"},
		{"utf-8 preserved", "héllo", "héllo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeMetadataValue(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeMetadataValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
