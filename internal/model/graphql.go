package model

import "strings"

// deleteVerbs are the name prefixes that mark an operation as destructive
// (SPEC §3.5 P6). Shared by GraphQL safety and lint rule L015.
var deleteVerbs = []string{"delete", "remove", "destroy", "purge", "drop", "revoke"}

// DeleteVerb reports whether name starts with a delete verb (delete, remove,
// destroy, purge, drop, revoke), ignoring case. A bare verb matches.
func DeleteVerb(name string) bool {
	lower := strings.ToLower(name)
	for _, v := range deleteVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	return false
}

// GraphQLSafety classifies a GraphQL root field and reports whether calling
// it is idempotent (SPEC §3.5 P6). A query is ReadOnly and idempotent; a
// mutation is Write, raised to Destructive when its field name starts with a
// delete verb; neither mutation case is idempotent. The heuristic only ever
// raises Safety. Any other operation is unrecognised and fails closed as
// Destructive.
func GraphQLSafety(operation, field string) (Safety, bool) {
	switch operation {
	case "query":
		return ReadOnly, true
	case "mutation":
		if DeleteVerb(field) {
			return Destructive, false
		}
		return Write, false
	default:
		return Destructive, false
	}
}
