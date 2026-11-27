package graphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

// maxTypeRefDepth caps how many ofType levels one introspection type
// reference may nest (lists inside lists, each optionally non-null).
const maxTypeRefDepth = 32

// The standard introspection result shape (graphql-js getIntrospectionQuery).
type introSchema struct {
	QueryType        *introNamed      `json:"queryType"`
	MutationType     *introNamed      `json:"mutationType"`
	SubscriptionType *introNamed      `json:"subscriptionType"`
	Types            []introType      `json:"types"`
	Directives       []introDirective `json:"directives"`
}

type introNamed struct {
	Name string `json:"name"`
}

type introType struct {
	Kind           string       `json:"kind"`
	Name           string       `json:"name"`
	Description    string       `json:"description"`
	SpecifiedByURL string       `json:"specifiedByURL"`
	IsOneOf        bool         `json:"isOneOf"`
	Fields         []introField `json:"fields"`
	InputFields    []introInput `json:"inputFields"`
	Interfaces     []introRef   `json:"interfaces"`
	EnumValues     []introEnum  `json:"enumValues"`
	PossibleTypes  []introRef   `json:"possibleTypes"`
}

type introField struct {
	Name              string       `json:"name"`
	Description       string       `json:"description"`
	Args              []introInput `json:"args"`
	Type              *introRef    `json:"type"`
	IsDeprecated      bool         `json:"isDeprecated"`
	DeprecationReason *string      `json:"deprecationReason"`
}

type introInput struct {
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Type              *introRef `json:"type"`
	DefaultValue      *string   `json:"defaultValue"`
	IsDeprecated      bool      `json:"isDeprecated"`
	DeprecationReason *string   `json:"deprecationReason"`
}

type introEnum struct {
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	IsDeprecated      bool    `json:"isDeprecated"`
	DeprecationReason *string `json:"deprecationReason"`
}

type introRef struct {
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	OfType *introRef `json:"ofType"`
}

type introDirective struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	IsRepeatable bool         `json:"isRepeatable"`
	Locations    []string     `json:"locations"`
	Args         []introInput `json:"args"`
}

// builtins holds the type and directive names gqlparser's prelude declares;
// an introspection result repeats them, and redeclaring one is an error.
var builtins = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	doc := validator.PreludeDocument()
	for _, d := range doc.Definitions {
		names[d.Name] = true
	}
	for _, d := range doc.Directives {
		names["@"+d.Name] = true
	}
	return names
})

// introspectionToSDL converts an introspection result to SDL text. Names are
// checked against the Name grammar, text is re-quoted and default values are
// re-rendered from a parse, so nothing from the input reaches the SDL raw.
func introspectionToSDL(data []byte) (string, error) {
	raw, err := introspectionRoot(data)
	if err != nil {
		return "", err
	}
	var s introSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("decoding __schema: %w", err)
	}
	if s.QueryType == nil {
		return "", errors.New("__schema has no queryType")
	}
	w := &strings.Builder{}
	w.WriteString("schema {\n")
	for _, root := range []struct {
		op string
		t  *introNamed
	}{{"query", s.QueryType}, {"mutation", s.MutationType}, {"subscription", s.SubscriptionType}} {
		if root.t == nil {
			continue
		}
		if !isName(root.t.Name) {
			return "", fmt.Errorf("%s root: invalid type name %q", root.op, root.t.Name)
		}
		fmt.Fprintf(w, "  %s: %s\n", root.op, root.t.Name)
	}
	w.WriteString("}\n")

	skip := builtins()
	for _, t := range s.Types {
		if strings.HasPrefix(t.Name, "__") || skip[t.Name] {
			continue
		}
		if err := writeType(w, t); err != nil {
			return "", fmt.Errorf("type %q: %w", t.Name, err)
		}
	}
	for _, d := range s.Directives {
		if skip["@"+d.Name] {
			continue
		}
		if err := writeDirective(w, d); err != nil {
			return "", fmt.Errorf("directive %q: %w", d.Name, err)
		}
	}
	return w.String(), nil
}

func writeType(w *strings.Builder, t introType) error {
	if !isName(t.Name) {
		return errors.New("invalid type name")
	}
	writeDesc(w, t.Description, "")
	switch t.Kind {
	case "SCALAR":
		w.WriteString("scalar " + t.Name)
		if t.SpecifiedByURL != "" {
			w.WriteString(" @specifiedBy(url: " + quote(t.SpecifiedByURL) + ")")
		}
		w.WriteString("\n")
	case "OBJECT", "INTERFACE":
		keyword, sep := "type ", " implements "
		if t.Kind == "INTERFACE" {
			keyword = "interface "
		}
		w.WriteString(keyword + t.Name)
		for _, ref := range t.Interfaces {
			if !isName(ref.Name) {
				return fmt.Errorf("invalid interface name %q", ref.Name)
			}
			w.WriteString(sep + ref.Name)
			sep = " & "
		}
		w.WriteString(" {\n")
		for _, f := range t.Fields {
			if err := writeField(w, f); err != nil {
				return fmt.Errorf("field %q: %w", f.Name, err)
			}
		}
		w.WriteString("}\n")
	case "UNION":
		w.WriteString("union " + t.Name)
		sep := " = "
		for _, ref := range t.PossibleTypes {
			if !isName(ref.Name) {
				return fmt.Errorf("invalid member name %q", ref.Name)
			}
			w.WriteString(sep + ref.Name)
			sep = " | "
		}
		w.WriteString("\n")
	case "ENUM":
		w.WriteString("enum " + t.Name + " {\n")
		for _, v := range t.EnumValues {
			if !isName(v.Name) {
				return fmt.Errorf("invalid enum value %q", v.Name)
			}
			writeDesc(w, v.Description, "  ")
			w.WriteString("  " + v.Name + deprecated(v.IsDeprecated, v.DeprecationReason) + "\n")
		}
		w.WriteString("}\n")
	case "INPUT_OBJECT":
		w.WriteString("input " + t.Name)
		if t.IsOneOf {
			w.WriteString(" @oneOf")
		}
		w.WriteString(" {\n")
		for _, in := range t.InputFields {
			w.WriteString("  ")
			if err := writeInput(w, in); err != nil {
				return fmt.Errorf("input field %q: %w", in.Name, err)
			}
			w.WriteString("\n")
		}
		w.WriteString("}\n")
	default:
		return fmt.Errorf("unknown kind %q", t.Kind)
	}
	return nil
}

func writeField(w *strings.Builder, f introField) error {
	if !isName(f.Name) {
		return errors.New("invalid field name")
	}
	writeDesc(w, f.Description, "  ")
	w.WriteString("  " + f.Name)
	if err := writeArgs(w, f.Args); err != nil {
		return err
	}
	ref, err := typeRef(f.Type)
	if err != nil {
		return err
	}
	w.WriteString(": " + ref + deprecated(f.IsDeprecated, f.DeprecationReason) + "\n")
	return nil
}

func writeArgs(w *strings.Builder, args []introInput) error {
	if len(args) == 0 {
		return nil
	}
	w.WriteString("(")
	for i, a := range args {
		if i > 0 {
			w.WriteString(", ")
		}
		if err := writeInput(w, a); err != nil {
			return fmt.Errorf("argument %q: %w", a.Name, err)
		}
	}
	w.WriteString(")")
	return nil
}

// writeInput writes one argument or input field: "desc" name: Type = default.
func writeInput(w *strings.Builder, in introInput) error {
	if !isName(in.Name) {
		return errors.New("invalid name")
	}
	ref, err := typeRef(in.Type)
	if err != nil {
		return err
	}
	if in.Description != "" {
		w.WriteString(quote(in.Description) + " ")
	}
	w.WriteString(in.Name + ": " + ref)
	if in.DefaultValue != nil {
		v, err := constValue(*in.DefaultValue)
		if err != nil {
			return err
		}
		w.WriteString(" = " + v)
	}
	w.WriteString(deprecated(in.IsDeprecated, in.DeprecationReason))
	return nil
}

func writeDirective(w *strings.Builder, d introDirective) error {
	if !isName(d.Name) {
		return errors.New("invalid directive name")
	}
	if len(d.Locations) == 0 {
		return errors.New("no locations")
	}
	for _, loc := range d.Locations {
		if !isName(loc) {
			return fmt.Errorf("invalid location %q", loc)
		}
	}
	writeDesc(w, d.Description, "")
	w.WriteString("directive @" + d.Name)
	if err := writeArgs(w, d.Args); err != nil {
		return err
	}
	if d.IsRepeatable {
		w.WriteString(" repeatable")
	}
	w.WriteString(" on " + strings.Join(d.Locations, " | ") + "\n")
	return nil
}

// typeRef renders an ofType chain such as [Int!]!, walking it iteratively and
// refusing chains deeper than maxTypeRefDepth.
func typeRef(r *introRef) (string, error) {
	var wrappers []string
	for depth := 0; ; depth++ {
		if r == nil {
			return "", errors.New("missing type reference")
		}
		if depth >= maxTypeRefDepth {
			return "", fmt.Errorf("type reference nested deeper than %d", maxTypeRefDepth)
		}
		if r.Kind != "LIST" && r.Kind != "NON_NULL" {
			break
		}
		wrappers = append(wrappers, r.Kind)
		r = r.OfType
	}
	if !isName(r.Name) {
		return "", fmt.Errorf("invalid type name %q", r.Name)
	}
	out := r.Name
	for _, w := range slices.Backward(wrappers) {
		if w == "LIST" {
			out = "[" + out + "]"
		} else {
			out += "!"
		}
	}
	return out, nil
}

// constValue parses an introspection defaultValue (a GraphQL constant literal)
// and returns it re-rendered, so the raw text never reaches the SDL.
func constValue(lit string) (string, error) {
	doc, err := parser.ParseSchema(&ast.Source{Name: "default", Input: "input P {\n  f: Int = " + lit + "\n}\n"})
	if err != nil || len(doc.Definitions) != 1 || len(doc.Extensions)+len(doc.Directives)+len(doc.Schema)+len(doc.SchemaExtension) != 0 {
		return "", errors.New("invalid default value")
	}
	fields := doc.Definitions[0].Fields
	if len(doc.Definitions[0].Directives) != 0 || len(fields) != 1 || fields[0].DefaultValue == nil || len(fields[0].Directives) != 0 {
		return "", errors.New("invalid default value")
	}
	return fields[0].DefaultValue.String(), nil
}

func writeDesc(w *strings.Builder, desc, indent string) {
	if desc != "" {
		w.WriteString(indent + quote(desc) + "\n")
	}
}

func deprecated(is bool, reason *string) string {
	switch {
	case !is:
		return ""
	case reason == nil:
		return " @deprecated"
	default:
		return " @deprecated(reason: " + quote(*reason) + ")"
	}
}

// quote renders s as a GraphQL string literal (gqlparser's own escaping).
func quote(s string) string {
	return (&ast.Value{Kind: ast.StringValue, Raw: s}).String()
}
