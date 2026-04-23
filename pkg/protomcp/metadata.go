package protomcp

import "strings"

// SanitizeMetadataValue strips ASCII control bytes (< 0x20 except tab)
// from v. HTTP/2 RFC 7540 §10.3 forbids CR/LF in header values;
// gRPC-go's metadata.MD.Set does not validate, and the HPACK encoder
// RST_STREAMs on such input.
func SanitizeMetadataValue(v string) string {
	if v == "" {
		return v
	}
	clean := true
	for i := range len(v) {
		if b := v[i]; b < 0x20 && b != '\t' {
			clean = false
			break
		}
	}
	if clean {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := range len(v) {
		c := v[i]
		if c < 0x20 && c != '\t' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
