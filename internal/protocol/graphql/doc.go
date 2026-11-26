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
// The ingest code is pure: no network, filesystem, clock, env or randomness.
package graphql
