package lint

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---- L007: more phrasings ----

var (
	// Instructions to the model that the base injection pattern doesn't
	// cover: other verbs, role changes, prompt extraction, tool shadowing,
	// secrecy, skipping confirmation, and a few other languages.
	injectionMore = regexp.MustCompile(`(?i)\b(` + strings.Join([]string{
		`(ignore|disregard|forget|override|bypass|skip|discard|drop|neglect|overlook)\s+(all|any|every|your|these|those|previous|prior|earlier|above|system|safety)\s+(\w+\s+){0,2}(instructions?|rules|prompts?|guidelines|guardrails|constraints|restrictions|polic(y|ies))`,
		`from\s+now\s+on,?\s+you\b`,
		`you\s+are\s+now\s+(an?\s+)?(unrestricted|jailbroken|dan\b|in\s+(developer|maintenance|admin|debug|god)\s+mode)`,
		`pretend\s+(to\s+be|you\s+are|that\s+you)`,
		`(reveal|print|show|repeat|output|leak|disclose|display)\s+(me\s+)?(your|the)\s+(system\s+prompt|hidden\s+(prompt|instructions?)|initial\s+instructions?|developer\s+message)`,
		`(always|must|first)\s+(call|invoke|use|run)\s+this\s+tool`,
		`before\s+(using|calling|invoking|you\s+(use|call))\s+(any\s+|every\s+|all\s+)?other\s+tools?`,
		`when(ever)?\s+(the\s+)?user\s+asks?\b[^.]{0,60}\b(also|instead|first)\s+(call|send|run|use|invoke|forward|upload)`,
		`(silently|secretly|covertly|quietly)\s+(send|call|run|delete|forward|upload|execute|add|include|post|copy)`,
		`without\s+the\s+user'?s?\s+(knowledge|consent|permission|approval)`,
		`user\s+(should|must|need)\s+not\s+(know|see|be\s+told|be\s+informed|notice)`,
		`do\s+not\s+ask\s+(the\s+user\s+)?(for\s+)?(confirmation|permission|approval)`,
		`(bypass|skip|disable|circumvent)\s+(the\s+)?((confirmation|approval)\s+(step|prompt|checks?|dialog|flow|requirement)|safety|guardrails?|security\s+checks?)`,
		`ignora(r)?\s+(todas\s+)?las\s+instrucciones\s+(anteriores|previas)`,
		`ignore[zr]?\s+(toutes\s+)?les\s+instructions\s+(pr[ée]c[ée]dentes|ant[ée]rieures)`,
		`ignorier(e|en)\s+(sie\s+)?(alle\s+)?(vorherigen|bisherigen|obigen)\s+anweisungen`,
	}, "|") + `)`)

	// Chat-template control tokens have no place in API documentation.
	chatTokens = regexp.MustCompile(`(?i)<\|(im_start|im_end|system|user|assistant|endoftext)\|>|\[/?INST\]|<<SYS>>|###\s*(system|assistant)\b`)

	// The patterns tried on de-obfuscated text. Role prefixes ("system:")
	// are left out: folding separators would invent them ("file-system:").
	foldedInjection = regexp.MustCompile(`\b((ignore|disregard|forget|override)\s+(all\s+|any\s+|the\s+|your\s+)*(previous|prior|above|earlier|preceding|system)\b|exfiltrat\w*|new\s+instructions?\b)`)

	// Text spelled out one letter at a time, with the spaces removed.
	spelledOut      = regexp.MustCompile(`(?:\b[a-z0-9] ){5,}[a-z0-9]\b`)
	spelledInjecton = regexp.MustCompile(`(ignore|disregard|forget|override)(all|any|the|your)*(previous|prior|above|earlier|preceding|system|instructions)`)

	// Reading local secrets: worth a look, but real docs mention these
	// files too (an SSH public key), so it is a warning.
	localSecrets = regexp.MustCompile(`(?i)\b(read|cat|open|include|send|upload|attach|print|copy)\b[^.]{0,40}?(~/\.ssh|\bid_(rsa|ed25519|ecdsa)\b|\.env\b|/etc/(passwd|shadow)|\.aws/credentials|\.git-credentials|\.netrc)`)

	base64Run = regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`)
	hexRun    = regexp.MustCompile(`\b(?:[0-9a-fA-F]{2}){16,}\b`)
)

// instruction returns the part of s that reads like an instruction to the
// model and how it was written ("" for plain text), or "" if there is none.
func instruction(s string) (match, how string) {
	for _, re := range []*regexp.Regexp{injection, injectionMore, chatTokens} {
		if m := re.FindString(s); m != "" {
			return m, ""
		}
	}
	f := fold(s)
	for _, re := range []*regexp.Regexp{foldedInjection, injectionMore} {
		if m := re.FindString(f); m != "" {
			return m, "obfuscated with lookalike characters, digits or separators"
		}
	}
	for _, run := range spelledOut.FindAllString(f, -1) {
		if m := spelledInjecton.FindString(strings.ReplaceAll(run, " ", "")); m != "" {
			return m, "spelled out letter by letter"
		}
	}
	return "", ""
}

// Letters of other alphabets that look like Latin ones.
var lookalikes = map[rune]rune{
	0x0430: 'a', 0x0435: 'e', 0x043e: 'o', 0x0440: 'p', 0x0441: 'c', 0x0445: 'x', 0x0443: 'y',
	0x0456: 'i', 0x0455: 's', 0x0458: 'j', 0x04bb: 'h', 0x0501: 'd', 0x043a: 'k', 0x043c: 'm',
	0x0442: 't', 0x0432: 'b', 0x043d: 'h', 0x0433: 'r',
	0x03bf: 'o', 0x03b1: 'a', 0x03b5: 'e', 0x03b9: 'i', 0x03ba: 'k', 0x03bd: 'v', 0x03c1: 'p',
	0x03c4: 't', 0x03c5: 'u', 0x03c7: 'x',
}

// Digits and symbols that stand in for letters.
var leet = map[rune]rune{'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's'}

// fold undoes common ways of disguising words: it lowercases, maps
// full-width and lookalike letters to Latin, reads digits inside words as
// the letters they stand for, and turns separators between letters into
// spaces. It is for matching only, never shown.
func fold(s string) string {
	rs := []rune(strings.ToLower(s))
	for i, r := range rs {
		switch {
		case r >= 0xff01 && r <= 0xff5e: // full-width forms
			rs[i] = unicode.ToLower(r - 0xfee0)
		case lookalikes[r] != 0:
			rs[i] = lookalikes[r]
		case unicode.IsSpace(r):
			rs[i] = ' '
		}
	}
	letter := func(i int) bool { return i >= 0 && i < len(rs) && rs[i] >= 'a' && rs[i] <= 'z' }
	alnum := func(i int) bool { return letter(i) || (i >= 0 && i < len(rs) && rs[i] >= '0' && rs[i] <= '9') }
	out := make([]rune, len(rs))
	copy(out, rs)
	for i, r := range rs {
		if l, ok := leet[r]; ok && (letter(i-1) || letter(i+1)) {
			out[i] = l
		} else if strings.ContainsRune("_-.*~|", r) && alnum(i-1) && alnum(i+1) {
			out[i] = ' '
		}
	}
	return string(out)
}

// mixedScript returns a word that mixes Latin letters with Cyrillic or
// Greek ones, the mark of a lookalike substitution, or "".
func mixedScript(s string) string {
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		latin, other := false, false
		for _, r := range w {
			switch {
			case unicode.Is(unicode.Latin, r):
				latin = true
			case unicode.Is(unicode.Cyrillic, r), unicode.Is(unicode.Greek, r):
				other = true
			}
		}
		if latin && other {
			return w
		}
	}
	return ""
}

// encodedText returns the readable texts hidden in s as base64 or hex.
func encodedText(s string) []string {
	var out []string
	add := func(b []byte, err error) {
		if err == nil && readable(b) {
			out = append(out, string(b))
		}
	}
	for _, run := range hexRun.FindAllString(s, -1) {
		add(hex.DecodeString(run))
	}
	for _, run := range base64Run.FindAllString(s, -1) {
		run = strings.TrimRight(run, "=")
		add(base64.RawStdEncoding.DecodeString(run))
		add(base64.RawURLEncoding.DecodeString(run))
	}
	return out
}

// readable reports whether b is a sentence: printable ASCII with spaces.
// Hashes, IDs and ordinary words that happen to decode don't qualify.
func readable(b []byte) bool {
	if len(b) < 12 || !utf8.Valid(b) {
		return false
	}
	spaces := 0
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
		if c == ' ' {
			spaces++
		}
	}
	return spaces >= 2
}

// ---- names (L015-L017, L019) ----

// words splits an identifier into lowercase words: camelCase, snake_case,
// kebab-case and dotted names all work.
func words(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))):
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func wordSet(list string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(list) {
		m[w] = true
	}
	return m
}

var (
	writeVerbs = wordSet(`delete remove destroy create update modify set send post pay transfer execute exec run
		reset revoke cancel approve purge drop write add insert patch put trigger start stop kill terminate disable
		enable rotate publish merge deploy restart shutdown wipe erase charge refund invite ban kick grant`)
	commandWords = wordSet(`command cmd exec shell script sql eval`)
	urlWords     = wordSet(`url uri webhook callback redirect endpoint host hostname`)

	// The credential word must end the name: "secret_name" names a secret.
	credentialName = regexp.MustCompile(`(^|_)(api_?key|access_?token|auth_?token|id_?token|refresh_?token|bearer(_token)?|` +
		`password|passwd|pwd|secret|client_?secret|private_?key|authorization|session_?(id|token)|credentials?)$`)
)

// writeLike returns the word that makes a read-only operation look like it
// changes something: the verb its name starts with, or a path segment.
func writeLike(name, path string) string {
	if w := words(name); len(w) > 0 && writeVerbs[w[0]] {
		return w[0]
	}
	for seg := range strings.SplitSeq(path, "/") {
		if s := strings.ToLower(seg); writeVerbs[s] {
			return s
		}
	}
	return ""
}

// riskyParam classifies a parameter by its name: "command" for something
// the upstream may execute, "target" for a URL, host or file it may reach
// for, or "".
func riskyParam(name string) string {
	w := words(name)
	file, path := false, false
	for _, x := range w {
		switch {
		case commandWords[x]:
			return "command"
		case urlWords[x]:
			return "target"
		}
		file, path = file || x == "file", path || x == "path"
	}
	if file && path {
		return "target"
	}
	return ""
}

// squash drops case and separators from a tool name.
func squash(name string) string {
	return strings.Join(words(name), "")
}
