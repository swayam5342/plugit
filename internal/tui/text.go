package tui

import (
	"strings"
	"unicode"
)

// safe strips control and format characters from untrusted text.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func truncate(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Safe strips control and format characters from untrusted text before it
// is printed to a terminal.
func Safe(s string) string { return safe(s) }

// SafeText is Safe for text of several lines: line breaks and tabs are
// kept, every other control or format character is removed.
func SafeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
