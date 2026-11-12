// Package clients installs PlugIt into MCP client configs (SPEC T13): Claude
// Desktop and Cursor (JSON files, edited in place) and Claude Code (through
// its own CLI). Only PlugIt's own entry ever changes; every other key and
// its order are kept. Entries carry a command and arguments only, never
// secrets.
package clients

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrInvalidConfig = errors.New("client config is not a JSON object plugit can edit safely")
	ErrNotOurs       = errors.New("an MCP server entry with that name exists and wasn't added by plugit")
)

// Entry is the MCP server entry plugit writes.
type Entry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type member struct {
	Key   string
	Value json.RawMessage
}

// parseObject reads a JSON object as ordered members. Values are kept as
// their original text. Anything but a single object (comments, trailing
// data) is an error.
func parseObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: expected an object", ErrInvalidConfig)
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: expected a key", ErrInvalidConfig)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		out = append(out, member{key, raw})
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the closing brace", ErrInvalidConfig)
	}
	return out, nil
}

// encodeObject writes members back, compactly; render indents the result.
func encodeObject(ms []member) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(marshalNoEscape(m.Key))
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func marshalNoEscape(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // strings and Entry always encode
	return bytes.TrimRight(b.Bytes(), "\n")
}

// render indents a document with two spaces (as Claude Desktop and Cursor
// write theirs). json.Indent only changes whitespace: numbers and string
// escapes keep their original text.
func render(doc json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, doc, "", "  "); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func find(ms []member, key string) int {
	for i, m := range ms {
		if m.Key == key {
			return i
		}
	}
	return -1
}

// servers returns the top-level members and the mcpServers members (nil if
// there's no mcpServers key yet).
func servers(data []byte) (top, srv []member, err error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, nil
	}
	if top, err = parseObject(data); err != nil {
		return nil, nil, err
	}
	if i := find(top, "mcpServers"); i >= 0 {
		if srv, err = parseObject(top[i].Value); err != nil {
			return nil, nil, fmt.Errorf("mcpServers: %w", err)
		}
	}
	return top, srv, nil
}

// InstallJSON adds or replaces the named server entry in a client config
// with an mcpServers object (Claude Desktop, Cursor). Empty input starts a
// new config. A same-named entry that isn't plugit's own for this API is
// ErrNotOurs.
func InstallJSON(data []byte, name string, e Entry) ([]byte, error) {
	top, srv, err := servers(data)
	if err != nil {
		return nil, err
	}
	val := marshalNoEscape(e)
	if i := find(srv, name); i >= 0 {
		if !isPlugitEntry(srv[i].Value, name) {
			return nil, fmt.Errorf("%w: %q", ErrNotOurs, name)
		}
		srv[i].Value = val
	} else {
		srv = append(srv, member{name, val})
	}
	if i := find(top, "mcpServers"); i >= 0 {
		top[i].Value = encodeObject(srv)
	} else {
		top = append(top, member{"mcpServers", encodeObject(srv)})
	}
	return render(encodeObject(top))
}

// UninstallJSON removes plugit's entry for name. removed is false when there
// was none. Someone else's entry of that name is ErrNotOurs.
func UninstallJSON(data []byte, name string) (out []byte, removed bool, err error) {
	top, srv, err := servers(data)
	if err != nil {
		return nil, false, err
	}
	i := find(srv, name)
	if i < 0 {
		return data, false, nil
	}
	if !isPlugitEntry(srv[i].Value, name) {
		return nil, false, fmt.Errorf("%w: %q", ErrNotOurs, name)
	}
	srv = append(srv[:i], srv[i+1:]...)
	top[find(top, "mcpServers")].Value = encodeObject(srv)
	out, err = render(encodeObject(top))
	return out, err == nil, err
}

// isPlugitEntry reports whether raw is a plugit serve entry for name.
func isPlugitEntry(raw json.RawMessage, name string) bool {
	var e Entry
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	base := strings.ToLower(e.Command[strings.LastIndexAny(e.Command, `/\`)+1:])
	if strings.TrimSuffix(base, filepath.Ext(base)) != "plugit" {
		return false
	}
	for i := 0; i+1 < len(e.Args); i++ {
		if e.Args[i] == "serve" && e.Args[i+1] == name {
			return true
		}
	}
	return false
}
