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

// StreamLess orders a project's workstreams: by status as issues.Rank has
// it, then priority, then number, then root.
func StreamLess(a, b *Stream) bool {
	if ra, rb := issues.Rank(a.Issue.Status), issues.Rank(b.Issue.Status); ra != rb {
		return ra < rb
	}
	if pa, pb := issues.PriorityRank(a.Issue.Priority), issues.PriorityRank(b.Issue.Priority); pa != pb {
		return pa < pb
	}
	if a.ID != b.ID {
		return a.ID < b.ID
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
