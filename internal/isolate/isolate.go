// Package isolate runs spec parsing in a child process (SPEC §5.3, T2).
//
// libopenapi parses on goroutines it starts itself, and a panic on one of
// them cannot be recovered: in-process, one hostile spec would crash plugit
// (pb33f/libopenapi#705). The parent re-executes the plugit binary with
// ChildArg, sends the spec on stdin and reads the manifest from stdout. A
// crash, malformed output, oversized output or a timeout in the child becomes
// an ordinary error in the parent.
//
// The child gets an almost empty environment, so secrets held in the
// parent's env vars never reach the code that handles untrusted input.
package isolate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/openapi"
)

// ChildArg is the hidden first argument that turns plugit into a parse child.
const ChildArg = "__parse"

// Defaults applied when the corresponding Runner field is zero.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultMaxOutput = 256 << 20
	maxRequest       = 64 << 20 // a 20 MiB spec, base64-encoded, plus options
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrParserCrashed = errors.New("spec parser crashed")
	ErrParseTimeout  = errors.New("spec parser timed out")
)

type request struct {
	Spec           []byte `json:"spec"`
	Prefix         string `json:"prefix,omitempty"`
	MaxDescription int    `json:"maxDescription,omitempty"`
}

type response struct {
	Manifest *model.Manifest `json:"manifest,omitempty"`
	Err      string          `json:"err,omitempty"`
	Kind     string          `json:"kind,omitempty"`
}

// errorKinds carries openapi's sentinel errors across the process boundary.
var errorKinds = []struct {
	kind string
	err  error
}{
	{"unsupported", openapi.ErrUnsupportedSpec},
	{"ref-too-deep", openapi.ErrRefTooDeep},
	{"ref-cycle", openapi.ErrRefCycle},
	{"panic", openapi.ErrParserPanic},
	{"invalid", openapi.ErrInvalidSpec},
}

// Runner starts parse children.
type Runner struct {
	Exe       string   // binary to run
	Args      []string // arguments, normally just ChildArg
	Env       []string // extra environment for the child (tests only)
	Timeout   time.Duration
	MaxOutput int64
}

// NewRunner returns a Runner that re-executes the current binary.
func NewRunner() (*Runner, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating plugit binary: %w", err)
	}
	return &Runner{Exe: exe, Args: []string{ChildArg}}, nil
}

// Parse is openapi.Parse in a child process. opts.Logger is not used.
func Parse(ctx context.Context, data []byte, opts openapi.Options) (*model.Manifest, error) {
	r, err := NewRunner()
	if err != nil {
		return nil, err
	}
	return r.Parse(ctx, data, opts)
}

// Parse runs one parse child.
func (r *Runner) Parse(ctx context.Context, data []byte, opts openapi.Options) (*model.Manifest, error) {
	timeout, maxOut := r.Timeout, r.MaxOutput
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}
	req, err := json.Marshal(request{Spec: data, Prefix: opts.Prefix, MaxDescription: opts.MaxDescription})
	if err != nil {
		return nil, fmt.Errorf("encoding parse request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Exe, r.Args...) // #nosec G204 -- our own binary, fixed arguments
	cmd.Env = append(minimalEnv(), r.Env...)
	cmd.Stdin = bytes.NewReader(req)
	stdout := &cappedBuffer{max: maxOut, onOverflow: cancel}
	stderr := &cappedBuffer{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	switch {
	case stdout.overflowed():
		return nil, fmt.Errorf("%w: output exceeded %d bytes", ErrParserCrashed, maxOut)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%w after %v", ErrParseTimeout, timeout)
	case runErr != nil:
		return nil, fmt.Errorf("%w: %w%s", ErrParserCrashed, runErr, firstLine(stderr.String()))
	}

	var resp response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("%w: malformed output: %w", ErrParserCrashed, err)
	}
	if resp.Err != "" {
		return nil, childError{msg: resp.Err, kind: resp.Kind}
	}
	if resp.Manifest == nil {
		return nil, fmt.Errorf("%w: empty result", ErrParserCrashed)
	}
	return resp.Manifest, nil
}

// ChildMain is the child side: it reads one request from stdin, parses it
// and writes one response to stdout. It returns the process exit code.
func ChildMain(stdin io.Reader, stdout io.Writer) int {
	var req request
	data, err := io.ReadAll(io.LimitReader(stdin, maxRequest+1))
	if err == nil && len(data) > maxRequest {
		err = errors.New("request too large")
	}
	if err == nil {
		err = json.Unmarshal(data, &req)
	}
	if err != nil {
		writeResponse(stdout, response{Err: "reading parse request: " + err.Error()})
		return 2
	}

	m, err := openapi.Parse(req.Spec, openapi.Options{Prefix: req.Prefix, MaxDescription: req.MaxDescription})
	if err != nil {
		resp := response{Err: err.Error()}
		for _, k := range errorKinds {
			if errors.Is(err, k.err) {
				resp.Kind = k.kind
				break
			}
		}
		writeResponse(stdout, resp)
		return 0
	}
	writeResponse(stdout, response{Manifest: m})
	return 0
}

func writeResponse(w io.Writer, r response) {
	b, err := json.Marshal(r)
	if err != nil {
		b = []byte(`{"err":"encoding parse response failed"}`)
	}
	_, _ = w.Write(b)
}

// childError is an error reported by the child. It unwraps to the matching
// openapi sentinel, so errors.Is works as it would in-process.
type childError struct {
	msg, kind string
}

func (e childError) Error() string { return e.msg }

func (e childError) Unwrap() error {
	for _, k := range errorKinds {
		if k.kind == e.kind {
			return k.err
		}
	}
	return nil
}

// minimalEnv is the child's environment: nothing from the parent except what
// Windows needs to load system DLLs.
func minimalEnv() []string {
	if runtime.GOOS == "windows" {
		if root := os.Getenv("SystemRoot"); root != "" {
			return []string{"SystemRoot=" + root}
		}
	}
	return []string{}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	line, _, _ := strings.Cut(s, "\n")
	return " (" + naming.CleanText(line, 200) + ")"
}

// cappedBuffer keeps at most max bytes; on overflow it records the fact and
// calls onOverflow (which kills the child).
type cappedBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int64
	over       bool
	onOverflow func()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.over {
		return 0, errors.New("output limit exceeded")
	}
	if int64(c.buf.Len()+len(p)) > c.max {
		c.over = true
		if c.onOverflow != nil {
			c.onOverflow()
		}
		return 0, errors.New("output limit exceeded")
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) overflowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

func (c *cappedBuffer) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Bytes()
}

func (c *cappedBuffer) String() string { return string(c.Bytes()) }
