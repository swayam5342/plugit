// Package naming turns operations into MCP tool names, IDs and descriptions
// (SPEC §3.5 rules 1 and 2). It is pure and deterministic: the same input
// always yields the same names, whatever order it arrives in.
package naming

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/swayam5342/plugit/internal/model"
)

// MaxNameLen is the longest MCP tool name PlugIt produces.
const MaxNameLen = 64

// DefaultMaxDescription caps tool descriptions, in runes.
const DefaultMaxDescription = 1024

// ToolID returns the first 8 hex characters of sha256(key).
func ToolID(key model.OpKey) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// Sanitize maps s onto [A-Za-z0-9_-]: other characters become '_', runs of
// '_' collapse, and leading or trailing '_' are trimmed. An empty result
// becomes "tool".
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastUnderscore := false
	for _, r := range s {
		ok := r == '-' || r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
		if !ok || r == '_' {
			if !lastUnderscore {
				b.WriteByte('_')
			}
			lastUnderscore = true
			continue
		}
		b.WriteRune(r)
		lastUnderscore = false
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tool"
	}
	return out
}

// BaseName is the unprefixed, sanitized name for an operation: its
// operationId, or "{method}_{path}" with braces removed.
func BaseName(operationID, method, path string) string {
	if strings.TrimSpace(operationID) != "" {
		return Sanitize(operationID)
	}
	p := strings.NewReplacer("{", "", "}", "").Replace(path)
	return Sanitize(strings.ToLower(method) + "_" + p)
}

// Candidate is one operation to name.
type Candidate struct {
	Key         model.OpKey
	OperationID string
}

// Assign names every candidate: optional prefix, sanitize, truncate to
// MaxNameLen, then dedupe case-insensitively with _2, _3, ... suffixes
// (truncating the base so the suffix fits). Candidates are processed in
// sorted OpKey order, so results don't depend on input order. Duplicate keys
// are named once.
func Assign(cands []Candidate, prefix string) map[model.OpKey]string {
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b Candidate) int { return cmp.Compare(a.Key, b.Key) })

	names := make(map[model.OpKey]string, len(sorted))
	used := make(map[string]bool, len(sorted))
	for _, c := range sorted {
		if _, done := names[c.Key]; done {
			continue
		}
		base := BaseName(c.OperationID, c.Key.Method(), c.Key.Path())
		if prefix != "" {
			base = Sanitize(prefix + "_" + base)
		}
		base = truncate(base, MaxNameLen)

		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			suffix := "_" + strconv.Itoa(n)
			name = truncate(base, MaxNameLen-len(suffix)) + suffix
		}
		used[strings.ToLower(name)] = true
		names[c.Key] = name
	}
	return names
}

// truncate cuts an already-sanitized (ASCII) name to n bytes and trims a
// trailing '_' left by the cut.
func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	if t := strings.TrimRight(s, "_"); t != "" {
		return t
	}
	return s
}

var (
	markdownLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTag      = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
)

// Describe picks a tool description: summary, then description, then
// "METHOD /path". Text is cleaned (HTML and markdown links stripped, control,
// bidi and other invisible format characters removed, whitespace collapsed)
// and capped at maxRunes (DefaultMaxDescription when maxRunes <= 0).
func Describe(summary, description, method, path string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxDescription
	}
	for _, s := range []string{summary, description} {
		if c := cleanText(s); c != "" {
			return capRunes(c, maxRunes)
		}
	}
	return capRunes(strings.ToUpper(method)+" "+path, maxRunes)
}

// CleanText applies Describe's cleaning to arbitrary spec text (parameter
// and schema descriptions, titles, warnings) and caps it at maxRunes
// (DefaultMaxDescription when maxRunes <= 0). It may return "".
func CleanText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxDescription
	}
	c := cleanText(s)
	if c == "" {
		return ""
	}
	return capRunes(c, maxRunes)
}

func cleanText(s string) string {
	s = markdownLink.ReplaceAllString(s, "$1")
	s = htmlComment.ReplaceAllString(s, "")
	s = htmlTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r): // Cf: bidi overrides, zero-width, tag chars
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
