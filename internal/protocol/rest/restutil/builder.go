package restutil

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Builder accumulates a REST manifest while a parser walks its source. The
// parser sets M's header fields (Format, SpecVersion, Title, Version,
// Endpoints) directly, adds tools with NewTool and AddOp, and calls Finish
// once at the end.
type Builder struct {
	// Opts are the parse options.
	Opts Options
	// M is the manifest being built.
	M model.Manifest

	ops        []*model.Tool
	opIDs      map[model.OpKey]string
	extraWarns int
}

// Warn adds a cleaned warning to the manifest. Past maxWarnings it only
// counts them, and Finish reports how many more there were.
func (b *Builder) Warn(msg string) {
	if len(b.M.Warnings) >= maxWarnings {
		b.extraWarns++
		return
	}
	b.M.Warnings = append(b.M.Warnings, naming.CleanText(msg, 300))
}

// Finish names the tools (naming.Assign, with Opts.Prefix), sorts them by
// key and returns the finished manifest.
func (b *Builder) Finish() *model.Manifest {
	b.M.Protocol = model.REST
	if b.extraWarns > 0 {
		b.M.Warnings = append(b.M.Warnings, fmt.Sprintf("... and %d more warnings", b.extraWarns))
	}
	cands := make([]naming.Candidate, 0, len(b.ops))
	for _, t := range b.ops {
		cands = append(cands, naming.Candidate{Key: t.Key, OperationID: b.opIDs[t.Key]})
	}
	names := naming.Assign(cands, b.Opts.Prefix)

	b.M.Tools = make([]model.Tool, 0, len(b.ops))
	for _, t := range b.ops {
		t.Name = names[t.Key]
		t.ID = naming.ToolID(t.Key)
		b.M.Tools = append(b.M.Tools, *t)
	}
	slices.SortFunc(b.M.Tools, func(x, y model.Tool) int { return cmp.Compare(x.Key, y.Key) })
	return &b.M
}

// AddOp registers a tool, ignoring duplicate keys (first wins).
func (b *Builder) AddOp(t *model.Tool, operationID string) {
	if b.opIDs == nil {
		b.opIDs = map[model.OpKey]string{}
	}
	if _, dup := b.opIDs[t.Key]; dup {
		b.Warn(fmt.Sprintf("duplicate operation %s ignored", t.Key))
		return
	}
	b.opIDs[t.Key] = operationID
	b.ops = append(b.ops, t)
}

// NewTool starts the tool for one operation: its key, its cleaned
// description and tags, and the safety and idempotency its method implies.
func (b *Builder) NewTool(method, path, summary, description string, tags []string, deprecated bool) *model.Tool {
	return &model.Tool{
		Key:         model.NewOpKey(method, path),
		Description: naming.Describe(summary, description, method, path, b.Opts.MaxDescription),
		Tags:        cleanTags(tags),
		Deprecated:  deprecated,
		Safety:      model.SafetyForMethod(method),
		Idempotent:  model.IdempotentForMethod(method),
		HTTP:        &model.HTTPOp{Method: strings.ToUpper(method), Path: path},
	}
}

const maxTagLen = 64

// cleanTags cleans tags like every other spec string: they reach the user's
// terminal (picker, hints), where an escape sequence could rewrite the
// screen. Empty and duplicate tags are dropped.
func cleanTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		if c := naming.CleanText(tag, maxTagLen); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// ValidPath rejects path templates that could smuggle a query or fragment,
// climb out of the pinned base path, or carry invisible text into tool keys
// and descriptions. Path templates come from the untrusted spec. The
// percent-decoded form is checked too, since a server decodes it. Hard
// problems always skip the path; risky ones skip it unless AllowUnsafePaths
// is on. A skipped path gets a warning.
func (b *Builder) ValidPath(path string) bool {
	decoded, err := url.PathUnescape(path)
	if err != nil {
		decoded = path
	}
	reason, risky := hardPathProblem(path, decoded), false
	if reason == "" {
		reason, risky = riskyPathProblem(path, decoded), true
	}
	switch {
	case reason == "":
		return true
	case risky && b.Opts.AllowUnsafePaths:
		return true
	}
	msg := fmt.Sprintf("path %+q skipped: %s", Truncate(path), reason)
	if risky {
		msg += "; to import it anyway, set network.allow_insecure (--allow-insecure)"
	}
	b.Warn(msg)
	return false
}

func hardPathProblem(path, decoded string) string {
	unprintable := func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || (unicode.IsSpace(r) && r != ' ')
	}
	switch {
	case !strings.HasPrefix(path, "/"):
		return "it must start with '/'"
	case strings.ContainsAny(path, "?#"):
		return "it contains '?' or '#'"
	case strings.IndexFunc(path, unprintable) >= 0:
		return "it contains control or invisible characters"
	case strings.IndexFunc(decoded, unprintable) >= 0:
		return "it contains percent-encoded control or invisible characters"
	}
	return ""
}

func riskyPathProblem(path, decoded string) string {
	dots := func(s string) bool {
		for seg := range strings.SplitSeq(s, "/") {
			if seg == "." || seg == ".." {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Contains(path, " "):
		return "it contains a space"
	case strings.Contains(path, `\`):
		return `it contains '\'`
	case dots(path):
		return "it contains a '.' or '..' segment"
	case strings.Contains(decoded, `\`):
		return `it contains a percent-encoded '\'`
	case dots(decoded):
		return "it contains a percent-encoded '.' or '..' segment"
	}
	return ""
}

// AddParam adds a parameter located in "path", "query", "header" or
// "cookie" (in) to t. Path parameters are always required. A parameter
// without a name, in an unknown location or with an invalid cookie name is
// dropped with an issue on c; hidden text in desc is reported there too.
func (b *Builder) AddParam(t *model.Tool, c *Collector, name, in string, required bool, desc string, schema json.RawMessage) {
	if name == "" {
		c.Issue(model.IssueSchemaDegraded, "parameter without a name (or an unresolvable parameter $ref) ignored")
		return
	}
	var loc model.Location
	switch strings.ToLower(in) {
	case "path":
		loc, required = model.InPath, true
	case "query":
		loc = model.InQuery
	case "header":
		loc = model.InHeader
	case "cookie":
		if !ValidCookieName(name) {
			c.Issue(model.IssueUnsupported, "%q isn't a valid cookie name; parameter dropped", name)
			return
		}
		loc = model.InCookie
	default:
		c.Issue(model.IssueUnsupported, "parameter %q in %q not supported", name, in)
		return
	}
	c.Hidden(fmt.Sprintf("parameter %q description", name), desc)
	t.Params = append(t.Params, model.Param{
		Name:     name,
		Bind:     model.Binding{Upstream: name, In: loc},
		Required: required,
		Schema:   schema,
		Desc:     naming.CleanText(desc, b.Opts.MaxDescription),
	})
}
