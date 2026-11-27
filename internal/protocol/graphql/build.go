package graphql

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Warning limits, as in the REST builder: a hostile schema must not grow the
// manifest without bound through warnings alone.
const (
	maxWarnings   = 25
	maxWarningLen = 300
	maxIssueLen   = 300
)

// maxParamNameLen caps model-facing parameter names (SPEC 3.5 rule 3; MCP
// property names match ^[a-zA-Z0-9_.-]{1,64}$).
const maxParamNameLen = 64

// operationPrefix starts every generated operation name: query PlugIt_user.
const operationPrefix = "PlugIt_"

// Build turns a loaded schema into a manifest: one tool per root Query and
// Mutation field, each with a fixed operation document that is validated
// against s before it is accepted (T18: the model only ever fills variables).
//
//   - Root fields whose names start with "__" (__schema, __type) never become
//     tools, so introspection is not exposed.
//   - Subscription fields become no tool, only a manifest warning (lint L013).
//   - A field whose generated document fails validation is dropped with a
//     manifest warning (add prints it; lint L013 reads it); the rest still
//     build.
//   - Names follow naming's rules with every query named before every
//     mutation, so "query user" keeps user and "mutation user" gets user_2.
//
// Manifest.GraphQL.NormalizedSDL is s printed back to SDL without built-ins.
// opts.ResponseFields (toolset -> tool key -> paths) is compiled into
// OverrideDocs, or into OverrideErrors for paths that don't compile (see
// compileOverrides). Title, Version and Endpoints stay empty: a schema
// carries none of them. The output is deterministic, tools sorted by key.
func Build(s *ast.Schema, format string, opts ingest.Options) (*model.Manifest, error) {
	if s == nil || s.Query == nil {
		return nil, fmt.Errorf("%w: schema has no query root type", ErrUnsupportedSpec)
	}
	sdl, err := normalizedSDL(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedSpec, err)
	}
	b := &builder{s: s, opts: opts, rules: rules.NewDefaultRules(), ops: map[model.OpKey]opParts{}}
	b.m = model.Manifest{
		Protocol: model.GraphQL,
		Format:   format,
		GraphQL:  &model.GraphQLSchema{NormalizedSDL: sdl},
	}

	var tools []*model.Tool
	for _, root := range []struct {
		op  string
		def *ast.Definition
	}{{"query", s.Query}, {"mutation", s.Mutation}} {
		for _, f := range rootFields(root.def) {
			t, err := b.tool(root.op, f)
			if err != nil {
				b.warn(fmt.Sprintf("%s %q skipped: the generated document failed validation: %v", root.op, f.Name, err))
				continue
			}
			tools = append(tools, t)
		}
	}
	for _, f := range rootFields(s.Subscription) {
		b.warn(fmt.Sprintf("subscription %q: subscriptions aren't supported yet (L013)", f.Name))
	}
	if b.extraWarns > 0 {
		b.m.Warnings = append(b.m.Warnings, fmt.Sprintf("... and %d more warnings", b.extraWarns))
	}

	// tools is in naming order: queries by key, then mutations by key.
	cands := make([]naming.Candidate, 0, len(tools))
	for _, t := range tools {
		cands = append(cands, naming.Candidate{Key: t.Key, OperationID: t.GraphQL.Field})
	}
	names := naming.AssignInOrder(cands, opts.Prefix)
	b.m.Tools = make([]model.Tool, 0, len(tools))
	for _, t := range tools {
		t.Name = names[t.Key]
		t.ID = naming.ToolID(t.Key)
		b.m.Tools = append(b.m.Tools, *t)
	}
	slices.SortFunc(b.m.Tools, func(x, y model.Tool) int { return cmp.Compare(x.Key, y.Key) })
	b.compileOverrides()
	return &b.m, nil
}

// compileOverrides compiles each toolset's response_fields for a tool into
// that tool's operation with the selection replaced (same operation name,
// variables and arguments), validated like the default document. A list
// that fails to compile or validate is recorded in OverrideErrors, cleaned,
// and leaves no document: the failure belongs to that toolset's entry, not
// to the schema, so the rest of the manifest still builds. A key that isn't
// one of the built tools is skipped (curation reports it as missing), as is
// an empty list. Paths arrive as GraphQL field paths: the CLI strips REST's
// [*] list markers first, since a selection follows lists anyway.
func (b *builder) compileOverrides() {
	for _, ts := range slices.Sorted(maps.Keys(b.opts.ResponseFields)) {
		byKey := b.opts.ResponseFields[ts]
		for _, key := range slices.Sorted(maps.Keys(byKey)) {
			paths := byKey[key]
			parts, ok := b.ops[model.OpKey(key)]
			if !ok || len(paths) == 0 {
				continue
			}
			doc, err := b.overrideDoc(parts, paths)
			if err != nil {
				setNested(&b.m.GraphQL.OverrideErrors, ts, key, naming.CleanText(err.Error(), maxIssueLen))
				continue
			}
			setNested(&b.m.GraphQL.OverrideDocs, ts, key, doc)
		}
	}
}

func (b *builder) overrideDoc(p opParts, paths []string) (string, error) {
	sel, err := SelectionFromPaths(b.s, p.ret, paths)
	if err != nil {
		return "", err
	}
	doc := document(p.op, p.field, p.vars, p.args, sel.Render())
	if err := b.validate(doc); err != nil {
		return "", fmt.Errorf("response_fields %s: the compiled document failed validation: %w", strings.Join(paths, ", "), err)
	}
	return doc, nil
}

func setNested(m *map[string]map[string]string, outer, inner, v string) {
	if *m == nil {
		*m = map[string]map[string]string{}
	}
	if (*m)[outer] == nil {
		(*m)[outer] = map[string]string{}
	}
	(*m)[outer][inner] = v
}

// opParts is what a tool's document is rendered from, kept so a
// response_fields override can be rendered the same way.
type opParts struct {
	op, field  string
	vars, args []string
	ret        *ast.Type
}

type builder struct {
	s          *ast.Schema
	opts       ingest.Options
	rules      *rules.Rules
	m          model.Manifest
	extraWarns int
	ops        map[model.OpKey]opParts
}

func (b *builder) warn(msg string) {
	if len(b.m.Warnings) >= maxWarnings {
		b.extraWarns++
		return
	}
	b.m.Warnings = append(b.m.Warnings, naming.CleanText(msg, maxWarningLen))
}

// rootFields returns def's fields sorted by name, without meta fields.
func rootFields(def *ast.Definition) []*ast.FieldDefinition {
	if def == nil {
		return nil
	}
	var out []*ast.FieldDefinition
	for _, f := range def.Fields {
		if f != nil && f.Type != nil && !strings.HasPrefix(f.Name, "__") {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(x, y *ast.FieldDefinition) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// tool builds the tool for the root field f of operation op ("query" or
// "mutation"), or returns the validation error of its document.
func (b *builder) tool(op string, f *ast.FieldDefinition) (*model.Tool, error) {
	key := model.OpKey(op + " " + f.Name)
	safety, idempotent := model.GraphQLSafety(op, f.Name)
	t := &model.Tool{
		Key:         key,
		Description: naming.CleanText(f.Description, b.opts.MaxDescription),
		Tags:        []string{op},
		Deprecated:  f.Directives.ForName("deprecated") != nil,
		Safety:      safety,
		Idempotent:  idempotent,
	}
	if t.Description == "" {
		t.Description = naming.CleanText(string(key), b.opts.MaxDescription)
	}
	issues := issueSet{t: t}
	issues.hidden("description", f.Description)

	// One mapper per tool: the schema node budget covers all its arguments.
	mapper := newArgMapper(b.s)
	used := map[string]bool{}
	var vars, args []string
	for _, a := range f.Arguments {
		if a == nil || a.Type == nil {
			continue
		}
		schema, required, degraded := mapper.arg(a.DefaultValue, a.Type)
		t.Params = append(t.Params, model.Param{
			Name:     uniqueName(a.Name, used),
			Bind:     model.Binding{In: model.InVariable, Upstream: a.Name},
			Required: required,
			Schema:   schema,
			Desc:     naming.CleanText(a.Description, b.opts.MaxDescription),
		})
		issues.hidden("argument "+a.Name+" description", a.Description)
		for _, d := range mapper.hidden() {
			issues.add(model.IssueHiddenText, d)
		}
		if degraded {
			issues.add(model.IssueSchemaDegraded, "argument "+a.Name+": schema simplified to {} (custom scalar, depth or node budget)")
		}
		v := "$" + a.Name + ": " + a.Type.String()
		if a.DefaultValue != nil {
			v += " = " + a.DefaultValue.String()
		}
		vars = append(vars, v)
		args = append(args, a.Name+": $"+a.Name)
	}

	sopts := SelectionOpts{Depth: b.opts.SelectionDepth, MaxFields: b.opts.MaxFields}
	sel, truncated := DefaultSelection(b.s, f.Type, sopts)
	if truncated {
		maxFields := sopts.MaxFields
		if maxFields <= 0 {
			maxFields = DefaultMaxFields
		}
		issues.add(model.IssueSelectionTruncated, fmt.Sprintf("selection hit graphql.max_fields (%d) and was truncated", maxFields))
	}

	doc := document(op, f.Name, vars, args, sel.Render())
	if err := b.validate(doc); err != nil {
		return nil, err
	}
	t.GraphQL = &model.GraphQLOp{Operation: op, Field: f.Name, Document: doc, Selection: sel.Paths()}
	b.ops[key] = opParts{op: op, field: f.Name, vars: vars, args: args, ret: f.Type}
	return t, nil
}

// document renders
// <op> PlugIt_<field>($a: T, $b: T! = <default>) { <field>(a: $a, b: $b) <selection> }
// leaving out the parentheses for a field without arguments and the
// selection for a scalar or enum return type.
func document(op, field string, vars, args []string, selection string) string {
	var d strings.Builder
	d.WriteString(op + " " + operationPrefix + field)
	if len(vars) > 0 {
		d.WriteString("(" + strings.Join(vars, ", ") + ")")
	}
	d.WriteString(" { " + field)
	if len(args) > 0 {
		d.WriteString("(" + strings.Join(args, ", ") + ")")
	}
	if selection != "" {
		d.WriteString(" " + selection)
	}
	d.WriteString(" }")
	return d.String()
}

// validate checks doc against the schema with gqlparser's default rules. A
// validator panic on a hostile schema counts as invalid.
func (b *builder) validate(doc string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("validator panic: %v", r)
		}
	}()
	if _, errs := gqlparser.LoadQueryWithRules(b.s, doc, b.rules); len(errs) > 0 {
		return errs
	}
	return nil
}

// issueSet adds deduplicated issues to one tool.
type issueSet struct {
	t    *model.Tool
	seen map[model.Issue]bool
}

func (is *issueSet) add(kind model.IssueKind, detail string) {
	i := model.Issue{Kind: kind, Detail: naming.CleanText(detail, maxIssueLen)}
	if is.seen == nil {
		is.seen = map[model.Issue]bool{}
	}
	if !is.seen[i] {
		is.seen[i] = true
		is.t.Issues = append(is.t.Issues, i)
	}
}

// hidden records that cleaning removed hidden content from the text s of
// where (lint L007), as the REST converter does.
func (is *issueSet) hidden(where, s string) {
	if r := naming.Hidden(s); r != "" {
		is.add(model.IssueHiddenText, where+" contained "+r+" (removed)")
	}
}

// uniqueName returns name cut to maxParamNameLen bytes, or that base with
// _2, _3, ... (cut further so the suffix fits) if a case-insensitive match
// is already in used, and records the result. GraphQL names are ASCII, so a
// byte cut is a character cut.
func uniqueName(name string, used map[string]bool) string {
	base := cutBytes(name, maxParamNameLen)
	out := base
	for n := 2; used[strings.ToLower(out)]; n++ {
		suffix := "_" + strconv.Itoa(n)
		out = cutBytes(base, maxParamNameLen-len(suffix)) + suffix
	}
	used[strings.ToLower(out)] = true
	return out
}

func cutBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// normalizedSDL prints s back to SDL, built-in types and directives and
// introspection fields left out, so it reloads with LoadSchema.
func normalizedSDL(s *ast.Schema) (sdl string, err error) {
	defer func() {
		if r := recover(); r != nil {
			sdl, err = "", errors.New("printing the schema failed")
		}
	}()
	var buf strings.Builder
	formatter.NewFormatter(&buf).FormatSchema(s)
	return buf.String(), nil
}
