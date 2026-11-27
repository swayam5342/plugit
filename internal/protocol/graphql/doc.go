// Package graphql is PlugIt's GraphQL protocol (SPEC §3.5, M8).
//
// This file set covers ingest: detecting and loading a GraphQL schema from an
// SDL document or an introspection result into a gqlparser *ast.Schema.
// Loading runs in the isolate child and treats its input as hostile (T18):
//
//   - SDL detection is a cheap first-token check (a type-system keyword or a
//     """ description) before any full parse, so other formats are rejected
//     without allocating;
//   - an introspection result is converted to SDL with every name checked
//     against the GraphQL name grammar, every description re-quoted, every
//     default value re-parsed as a constant and re-rendered, and ofType
//     chains capped at maxTypeRefDepth, then loaded through the same
//     gqlparser path as SDL;
//   - a gqlparser panic becomes an error wrapping ErrUnsupportedSpec.
//
// An argMapper (one per tool) maps each field argument's GraphQL type to the
// JSON Schema of a tool parameter, capped at maxSchemaDepth levels per
// argument and a maxSchemaNodes node budget shared by all of the tool's
// arguments, and degrading unmappable parts (custom scalars included) to {}.
// ArgSchema is the single-argument shorthand.
//
// DefaultSelection builds the fixed selection set of a tool's document from
// its return type (P7): depth- and field-capped, spending the field budget
// breadth-first, never emitting an empty selection set, and aliasing field
// names that an interface's or union's possible types declare with different
// types. SelectionFromPaths compiles a toolset's response_fields into one
// instead, with a step cap so paths through nested unions cannot expand
// without bound.
//
// The ingest code is pure: no network, filesystem, clock, env or randomness.
//
// protocol.go is the serving side, the GraphQL protocol.Protocol: Encode
// POSTs the tool's fixed document with the validated arguments as variables
// and attaches no credential; Decode applies the P7 result rules (ok, partial,
// errors, else the HTTP status). It never dials or resolves a secret (T17).
package graphql
