package isolate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
)

// The test binary doubles as the child process: PLUGIT_TEST_CHILD selects
// what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("PLUGIT_TEST_CHILD") {
	case "":
		os.Exit(m.Run())
	case "parse":
		os.Exit(ChildMain(os.Stdin, os.Stdout))
	case "hang":
		time.Sleep(time.Hour)
	case "garbage":
		_, _ = os.Stdout.WriteString("this is not JSON")
		os.Exit(0)
	case "flood":
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range 64 {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(1)
			}
		}
		os.Exit(0)
	case "crash":
		done := make(chan struct{})
		go func() { panic("boom from a library goroutine") }()
		<-done
	case "dumpenv":
		b, _ := json.Marshal(response{Err: strings.Join(os.Environ(), "\n")})
		_, _ = os.Stdout.Write(b)
		os.Exit(0)
	}
	os.Exit(3)
}

func runner(mode string) *Runner {
	return &Runner{
		Exe:  os.Args[0],
		Args: []string{"-test.run=^$"},
		Env:  []string{"PLUGIT_TEST_CHILD=" + mode},
	}
}

const spec = `
openapi: 3.0.3
info: {title: Pets, version: '1'}
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses: {'200': {description: ok}}
    put:
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {name: {type: string}}}
      responses: {'200': {description: ok}}
`

func TestParseMatchesInProcess(t *testing.T) {
	opts := openapi.Options{Prefix: "pets", MaxDescription: 100}
	want, err := openapi.Parse([]byte(spec), opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner("parse").Parse(t.Context(), []byte(spec), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if !bytes.Equal(wb, gb) {
		t.Errorf("child result differs from in-process parse:\n got %s\nwant %s", gb, wb)
	}
	if got.Tools[0].Safety != model.ReadOnly && got.Tools[1].Safety != model.ReadOnly {
		t.Error("enums did not survive the round trip")
	}
}

func TestParseKeepsSentinelErrors(t *testing.T) {
	var chain strings.Builder
	chain.WriteString("openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /x:\n    get:\n      parameters:\n        - {name: a, in: query, schema: {$ref: '#/components/schemas/S0'}}\n      responses: {'200': {description: ok}}\ncomponents:\n  schemas:\n")
	for i := range 40 {
		chain.WriteString("    S" + itoa(i) + ": {$ref: '#/components/schemas/S" + itoa(i+1) + "'}\n")
	}
	chain.WriteString("    S40: {type: string}\n")

	for name, tc := range map[string]struct {
		src  string
		want error
	}{
		"deep ref":    {chain.String(), openapi.ErrRefTooDeep},
		"unsupported": {"asyncapi: 2.6.0\ninfo: {title: t, version: '1'}\nchannels: {}\n", openapi.ErrUnsupportedSpec},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runner("parse").Parse(t.Context(), []byte(tc.src), openapi.Options{})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// libopenapi v0.42.5 panics on a goroutine it starts itself for this input
// (pb33f/libopenapi#705). openapi's pre-check now rejects it before
// libopenapi sees it; crashes nobody has found yet are covered by the
// "crash" child in TestParseChildFailures.
const libopenapiCrasher = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /e:
    post:
      requestBody:
        content:
          n:
          a: {$ref: x}
      responses: {}
`

func TestParseKnownLibopenapiCrasherIsRejected(t *testing.T) {
	_, err := runner("parse").Parse(t.Context(), []byte(libopenapiCrasher), openapi.Options{})
	if !errors.Is(err, openapi.ErrInvalidSpec) {
		t.Fatalf("err = %v, want openapi.ErrInvalidSpec", err)
	}
}

func TestParseChildFailures(t *testing.T) {
	for mode, want := range map[string]error{
		"crash":   ErrParserCrashed,
		"garbage": ErrParserCrashed,
		"flood":   ErrParserCrashed,
	} {
		t.Run(mode, func(t *testing.T) {
			r := runner(mode)
			r.MaxOutput = 1 << 20
			_, err := r.Parse(t.Context(), []byte(spec), openapi.Options{})
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want %v", err, want)
			}
		})
	}
}

func TestParseTimeout(t *testing.T) {
	r := runner("hang")
	r.Timeout = 500 * time.Millisecond
	start := time.Now()
	_, err := r.Parse(t.Context(), []byte(spec), openapi.Options{})
	if !errors.Is(err, ErrParseTimeout) {
		t.Errorf("err = %v, want ErrParseTimeout", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
}

func TestChildGetsNoParentEnvironment(t *testing.T) {
	t.Setenv("PLUGIT_SECRET_FOR_TEST", "test-token-123")
	t.Setenv("GITHUB_TOKEN", "test-token-456")
	_, err := runner("dumpenv").Parse(t.Context(), []byte(spec), openapi.Options{})
	if err == nil {
		t.Fatal("dumpenv child returns its environment as an error")
	}
	for _, secret := range []string{"test-token-123", "test-token-456", "PLUGIT_SECRET_FOR_TEST", "GITHUB_TOKEN"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("child environment contains %q", secret)
		}
	}
}

func TestChildMainProtocolErrors(t *testing.T) {
	var out bytes.Buffer
	if code := ChildMain(strings.NewReader("not json"), &out); code == 0 {
		t.Error("ChildMain must fail on a malformed request")
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
