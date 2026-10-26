package world

import (
	"strconv"
	"strings"

	"overview/internal/fleet"
	"overview/internal/issues"
)

// NameLess orders project names alphabetically, ignoring case, with the
// exact spelling as the tiebreak so the order is total.
func NameLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// StartedLess orders sessions newest started first, then by session id.
// A session's start never changes, so the projects' rows keep their places
// while their states change under the cursor.
func StartedLess(a, b fleet.Row) bool {
	if a.StartedAt != b.StartedAt {
		return a.StartedAt > b.StartedAt
	}
	return a.SessionID < b.SessionID
}

// ActiveLess puts working sessions first, then the newest activity, then
// orders by session id.
func ActiveLess(a, b fleet.Row) bool {
	wa, wb := a.State == fleet.Working, b.State == fleet.Working
	if wa != wb {
		return wa
	}
	if !a.Since.Equal(b.Since) {
		return a.Since.After(b.Since)
	}
	return a.SessionID < b.SessionID
}

// needsFirst puts the sessions that need you first, longest wait first,
// then the rest as ActiveLess orders them.
func needsFirst(a, b fleet.Row) bool {
	na, nb := a.NeedsYou(), b.NeedsYou()
	switch {
	case na != nb:
		return na
	case !na:
		return ActiveLess(a, b)
	case !a.Since.Equal(b.Since):
		return a.Since.Before(b.Since)
	}
	return a.SessionID < b.SessionID
}

// IssueLess orders issues as the view shows them: by status as
// issues.Rank has it, so what waits on you comes first, then priority,
// then number. The cockpit's issues card lists a project's issues in this
// order.
func IssueLess(a, b issues.Issue) bool {
	if ra, rb := issues.Rank(a.Status), issues.Rank(b.Status); ra != rb {
		return ra < rb
	}
	return priorityLess(a, b)
}

// StartLess orders issues to start work on, as the task picker offers
// them: ready, then inbox, which are what there is to start, then the
// rest as IssueLess orders them.
func StartLess(a, b issues.Issue) bool {
	if ra, rb := startRank(a.Status), startRank(b.Status); ra != rb {
		return ra < rb
	}
	return priorityLess(a, b)
}

// startRank ranks a status for StartLess, lowest first.
func startRank(status string) int {
	switch status {
	case issues.Ready:
		return 0
	case issues.Inbox:
		return 1
	}
	return 2 + issues.Rank(status)
}

// priorityLess orders issues by priority, then number.
func priorityLess(a, b issues.Issue) bool {
	if pa, pb := issues.PriorityRank(a.Priority), issues.PriorityRank(b.Priority); pa != pb {
		return pa < pb
	}
	return a.ID < b.ID
}

// StreamLess orders a project's workstreams: by their issues as IssueLess
// orders them, then root.
func StreamLess(a, b *Stream) bool {
	switch {
	case IssueLess(a.Issue, b.Issue):
		return true
	case IssueLess(b.Issue, a.Issue):
		return false
	}
	return a.Root < b.Root
}

// WindowLess orders tmux window ids ("@7") by number, then as text.
func WindowLess(a, b string) bool {
	na, errA := strconv.Atoi(strings.TrimPrefix(a, "@"))
	nb, errB := strconv.Atoi(strings.TrimPrefix(b, "@"))
	if errA == nil && errB == nil && na != nb {
		return na < nb
	}
	if (errA == nil) != (errB == nil) {
		return errA == nil
	}
	return a < b
}
