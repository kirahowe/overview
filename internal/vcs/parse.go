package vcs

import (
	"strconv"
	"strings"
	"time"
)

// Op is one jj operation: its id and when it ended.
type Op struct {
	ID  string
	End time.Time
}

// ParseOpLog reads lines of "<id> <unix seconds>", newest first, as the
// op log template writes them. A line that does not fit is skipped.
func ParseOpLog(out string) []Op {
	var ops []Op
	for _, line := range strings.Split(out, "\n") {
		id, secs, ok := strings.Cut(strings.TrimSpace(line), " ")
		n, err := strconv.ParseInt(strings.TrimSpace(secs), 10, 64)
		if !ok || id == "" || err != nil {
			continue
		}
		ops = append(ops, Op{id, time.Unix(n, 0)})
	}
	return ops
}

// OpIndexAt picks from ops, newest first, the first that ended at or
// before t, and returns its index. When none is that old it picks the
// oldest. ok is false for no ops.
func OpIndexAt(ops []Op, t time.Time) (int, bool) {
	for i, op := range ops {
		if !op.End.After(t) {
			return i, true
		}
	}
	return len(ops) - 1, len(ops) > 0
}

// ReflogEntry is a commit HEAD moved to, and when.
type ReflogEntry struct {
	SHA  string
	Time time.Time
}

// ParseReflog reads lines of "<sha> HEAD@{<unix seconds>}", newest first.
// A line that does not fit is skipped.
func ParseReflog(out string) []ReflogEntry {
	var entries []ReflogEntry
	for _, line := range strings.Split(out, "\n") {
		sha, sel, ok := strings.Cut(strings.TrimSpace(line), " ")
		_, rest, found := strings.Cut(sel, "@{")
		secs, closed := strings.CutSuffix(rest, "}")
		n, err := strconv.ParseInt(secs, 10, 64)
		if !ok || sha == "" || !found || !closed || err != nil {
			continue
		}
		entries = append(entries, ReflogEntry{sha, time.Unix(n, 0)})
	}
	return entries
}

// ReflogAt picks the first entry, newest first, at or before t.
func ReflogAt(entries []ReflogEntry, t time.Time) (string, bool) {
	for _, e := range entries {
		if !e.Time.After(t) {
			return e.SHA, true
		}
	}
	return "", false
}
