// Package utils holds small, dependency-free helpers shared across the
// bridge — text formatting for logs, a CLI fatal-exit, and the slog
// handler used by every command.
package utils

// Sanitize replaces whitespace control characters (newlines, tabs)
// with single spaces so a message body fits on one log line.
func Sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// TruncateRunes shortens s to at most max runes, appending an ellipsis
// when it had to cut.
func TruncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

// DisplayTitle returns a single-line, max-N-rune preview of s,
// suitable for inclusion in log lines.
func DisplayTitle(s string, max int) string {
	return TruncateRunes(Sanitize(s), max)
}
