package graphql

import (
	"cmp"
	"errors"
	"fmt"
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
)

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
//     manifest warning; the rest still build.
//   - Names follow naming's rules with every query named before every
//     mutation, so "query user" keeps user and "mutation user" gets user_2.
//
// Manifest.GraphQL.NormalizedSDL is s printed back to SDL without built-ins;
// OverrideDocs is left nil. Title, Version and Endpoints stay empty: a schema
// carries none of them. The output is deterministic, tools sorted by key.
func Build(s *ast.Schema, format string, opts ingest.Options) (*model.Manifest, error) {
	if s == nil || s.Query == nil {
		return nil, fmt.Errorf("%w: schema has no query root type", ErrUnsupportedSpec)
	}
	sdl, err := normalizedSDL(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedSpec, err)
	}
	b := &builder{s: s, opts: opts, rules: rules.NewDefaultRules()}
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
	return &b.m, nil
}

type builder struct {
	s          *ast.Schema
	opts       ingest.Options
	rules      *rules.Rules
	m          model.Manifest
	extraWarns int
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
		if degraded {
			t.Issues = append(t.Issues, model.Issue{
				Kind:   model.IssueSchemaDegraded,
				Detail: "argument " + a.Name + ": schema simplified to {} (custom scalar, depth or node budget)",
			})
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
		t.Issues = append(t.Issues, model.Issue{
			Kind:   model.IssueSelectionTruncated,
			Detail: fmt.Sprintf("selection hit graphql.max_fields (%d) and was truncated", maxFields),
		})
	}

	doc := document(op, f.Name, vars, args, sel.Render())
	if err := b.validate(doc); err != nil {
		return nil, err
	}
	t.GraphQL = &model.GraphQLOp{Operation: op, Field: f.Name, Document: doc, Selection: sel.Paths()}
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

// uniqueName returns name, or name_2, name_3, ... if a case-insensitive
// match is already in used, and records the result.
func uniqueName(name string, used map[string]bool) string {
	out := name
	for n := 2; used[strings.ToLower(out)]; n++ {
		out = name + "_" + strconv.Itoa(n)
	}
	used[strings.ToLower(out)] = true
	return out
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
