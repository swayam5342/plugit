package model

import "testing"

func TestGraphQLSafety(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		field     string
		want      Safety
		wantIdem  bool
	}{
		{"query is read-only", "query", "user", ReadOnly, true},
		{"query with delete-verb field stays read-only", "query", "deletedUsers", ReadOnly, true},
		{"mutation is write", "mutation", "createIssue", Write, false},
		{"delete mutation is destructive", "mutation", "deleteRepository", Destructive, false},
		{"revoke mutation is destructive", "mutation", "revokeToken", Destructive, false},
		{"unrecognised operation fails closed", "subscription", "onEvent", Destructive, false},
		{"empty operation fails closed", "", "user", Destructive, false},
		// Operation names are matched case-sensitively, as GraphQL spells them.
		{"capitalised Query fails closed", "Query", "user", Destructive, false},
		{"capitalised Mutation fails closed", "Mutation", "createIssue", Destructive, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, idem := GraphQLSafety(tt.operation, tt.field)
			if got != tt.want || idem != tt.wantIdem {
				t.Errorf("GraphQLSafety(%q, %q) = (%v, %v), want (%v, %v)",
					tt.operation, tt.field, got, idem, tt.want, tt.wantIdem)
			}
		})
	}
}

func TestDeleteVerb(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"delete", true},
		{"Delete", true},
		{"deleteX", true},
		{"DeleteRepository", true},
		{"removeX", true},
		{"destroySession", true},
		{"purgeCache", true},
		{"DROPTable", true},
		{"revokeToken", true},
		{"createX", false},
		{"updateDeleted", false},
		{"undelete", false},
		// A prefix match over-matches on purpose: P6 may only raise Safety,
		// so a false positive costs a confirmation, never a leak.
		{"dropdownOptions", true},
		{"removed", true},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeleteVerb(tt.name); got != tt.want {
				t.Errorf("DeleteVerb(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
