package restutil

import (
	"fmt"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Collector gathers one tool's import issues, cleaned and deduplicated, in
// first-seen order. The zero value is ready to use.
type Collector struct {
	issues   []model.Issue
	issueSet map[string]bool
}

// Issue records an issue of kind with a printf-style detail. The detail is
// cleaned (it can quote spec text) and an exact repeat is dropped.
func (c *Collector) Issue(kind model.IssueKind, format string, args ...any) {
	detail := naming.CleanText(fmt.Sprintf(format, args...), 300)
	if c.issueSet == nil {
		c.issueSet = map[string]bool{}
	}
	if key := string(kind) + "\x00" + detail; !c.issueSet[key] {
		c.issueSet[key] = true
		c.issues = append(c.issues, model.Issue{Kind: kind, Detail: detail})
	}
}

// Hidden records that cleaning removed hidden content from where (L007).
func (c *Collector) Hidden(where, s string) {
	if r := naming.Hidden(s); r != "" {
		c.Issue(model.IssueHiddenText, "%s contained %s (removed)", where, r)
	}
}

// Issues returns the recorded issues, or nil if there are none.
func (c *Collector) Issues() []model.Issue {
	return c.issues
}
