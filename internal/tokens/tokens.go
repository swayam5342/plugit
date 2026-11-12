// Package tokens estimates how many tokens a toolset costs in the model's
// context (SPEC §10 Q2: a calibrated heuristic). Claude's tokenizer isn't
// public, so every count is an estimate and is shown with "≈". The package
// is pure: no I/O.
//
// The heuristic walks the JSON a client sends for each tool (name,
// description, input schema) and charges each run of characters roughly
// what byte-pair tokenizers charge for it: a common word is one token (with
// its leading space), long identifiers split every few letters, digits go
// in threes, JSON punctuation merges in pairs, and non-ASCII text costs a
// token per character.
package tokens

import (
	"encoding/json"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

// Calibration constants. Adjust them against measured counts, not by feel.
const (
	wordLen       = 7 // letters a single token usually covers
	longWordChunk = 5 // letters per token beyond that
	digitChunk    = 3
	punctChunk    = 2
	toolOverhead  = 8 // per-tool framing the client adds around name/description/schema
)

// Text estimates the tokens in s.
func Text(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r >= utf8.RuneSelf:
			n++
			i += size
		case isLetter(s[i]): // r is ASCII here, so s[i] is r
			j := i
			for j < len(s) && isLetter(s[j]) {
				// Split camelCase: a lowercase letter followed by an uppercase one.
				if j > i && isUpper(s[j]) && isLower(s[j-1]) {
					break
				}
				j++
			}
			n += wordTokens(j - i)
			i = j
		case isDigit(s[i]):
			j := i
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			n += ceilDiv(j-i, digitChunk)
			i = j
		case r == ' ':
			// A single space joins the next word; extra spaces cost.
			j := i
			for j < len(s) && s[j] == ' ' {
				j++
			}
			n += ceilDiv(j-i-1, 4)
			i = j
		case unicode.IsSpace(r):
			n++
			i++
		default:
			j := i
			for j < len(s) && s[j] < utf8.RuneSelf && isPunct(s[j]) {
				j++
			}
			n += ceilDiv(j-i, punctChunk)
			i = j
		}
	}
	return n
}

func wordTokens(l int) int {
	if l <= wordLen {
		return 1
	}
	return 1 + ceilDiv(l-wordLen, longWordChunk)
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func isLetter(c byte) bool { return isLower(c) || isUpper(c) }
func isLower(c byte) bool  { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool  { return c >= 'A' && c <= 'Z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isPunct(c byte) bool {
	return !isLetter(c) && !isDigit(c) && c != ' ' && c != '\n' && c != '\t' && c != '\r'
}

// Tool estimates one tool's cost: the payload a client sends the model
// (name, description and the input schema, which leaves out hidden params).
func Tool(t model.Tool) int {
	payload, err := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}{t.Name, t.Description, schema.InputSchema(t)})
	if err != nil {
		return toolOverhead
	}
	return toolOverhead + Text(string(payload))
}

// Total estimates a toolset's cost.
func Total(tools []model.Tool) int {
	n := 0
	for _, t := range tools {
		n += Tool(t)
	}
	return n
}

// Format renders an estimate for display: ≈850, ≈3.2k, ≈12k.
func Format(n int) string {
	switch {
	case n < 1000:
		return "≈" + strconv.Itoa(n)
	case n < 9950:
		return "≈" + strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	default:
		return "≈" + strconv.Itoa((n+500)/1000) + "k"
	}
}
