package clients

import (
	"bytes"
	"fmt"
	"strings"
)

// maxDiffCells bounds the line-diff table; past it, Diff summarizes.
const maxDiffCells = 4_000_000

// Diff returns a line diff of before and after: "- " for removed lines and
// "+ " for added ones, "  " for one line of context around each change, and
// "…" between distant changes. Identical inputs give "".
func Diff(before, after []byte) string {
	if bytes.Equal(before, after) {
		return ""
	}
	a, b := lines(before), lines(after)
	if len(a)*len(b) > maxDiffCells {
		return fmt.Sprintf("(file changed: %d lines before, %d after)\n", len(a), len(b))
	}
	// lcs[i][j] = length of the LCS of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, op{'+', b[j]})
			j++
		default:
			ops = append(ops, op{'-', a[i]})
			i++
		}
	}
	near := func(k int) bool {
		for d := -1; d <= 1; d++ {
			if k+d >= 0 && k+d < len(ops) && ops[k+d].kind != ' ' {
				return true
			}
		}
		return false
	}
	var out strings.Builder
	gap := false
	for k, o := range ops {
		if !near(k) {
			gap = true
			continue
		}
		if gap && out.Len() > 0 {
			out.WriteString("…\n")
		}
		gap = false
		out.WriteByte(o.kind)
		out.WriteByte(' ')
		out.WriteString(o.text)
		out.WriteByte('\n')
	}
	return out.String()
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
