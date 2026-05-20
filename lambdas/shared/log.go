package shared

import "unicode/utf8"

// TruncForLog truncates a string to n valid utf-8 bytes, appending "..."
// if truncated. Safe for strings containing accented characters.
func TruncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Step back to avoid splitting a multi-byte rune.
	trunc := s[:n]
	for !utf8.ValidString(trunc) && len(trunc) > 0 {
		trunc = trunc[:len(trunc)-1]
	}
	return trunc + "..."
}
