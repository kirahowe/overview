package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/ghostty"
)

// focusTimeout and locateTimeout bound each call to Ghostty, so a stuck
// osascript cannot hang a jump for long. Locate gets room for all its rounds.
const (
	focusTimeout  = 1500 * time.Millisecond
	locateTimeout = ghostty.LocateBudget + 300*time.Millisecond
)

// jumper is the part of ghostty.Client the view needs. Tests fake it.
type jumper interface {
	InGhostty(ctx context.Context, pid int) (bool, error)
	Locate(ctx context.Context, tty string) (string, error)
	Focus(ctx context.Context, id string) error
}

// ttyNote is the fallback for a session that cannot be jumped to: say where
// it runs. why, if set, says why the jump did not happen.
func ttyNote(name, tty, why string) string {
	where := "its own terminal"
	if tty != "" {
		where = tty
	}
	if why != "" {
		why = " (" + why + ")"
	}
	return name + " is interactive, in " + where + why + ". Switch to that tab to answer it."
}

func jumpReason(err error) string {
	switch {
	case errors.Is(err, ghostty.ErrNotRunning):
		return "Ghostty is not running"
	case errors.Is(err, ghostty.ErrNotFound):
		return "terminal not found"
	case errors.Is(err, context.DeadlineExceeded):
		return "Ghostty did not answer"
	}
	return "jump failed"
}

// jumpCmd focuses the terminal for tty. With a cached id it tries that first
// and, if Ghostty has forgotten it, locates once more. Locate writes to the
// tty, so it runs only for a session whose process pid runs under Ghostty.
// It returns a jumpMsg and never touches the model.
func jumpCmd(j jumper, seq int, name, key, tty string, pid int, cached string) tea.Cmd {
	return func() tea.Msg {
		call := func(timeout time.Duration, f func(ctx context.Context) error) error {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			return f(ctx)
		}
		msg := jumpMsg{seq: seq, key: key}
		id := cached
		if id != "" {
			err := call(focusTimeout, func(ctx context.Context) error { return j.Focus(ctx, id) })
			if err == nil {
				return msg
			}
			if !errors.Is(err, ghostty.ErrStale) {
				msg.note = ttyNote(name, tty, jumpReason(err))
				return msg
			}
			msg.drop = true
		}
		var inside bool
		err := call(focusTimeout, func(ctx context.Context) (e error) { inside, e = j.InGhostty(ctx, pid); return })
		if err == nil && !inside {
			msg.drop = true
			msg.note = ttyNote(name, tty, "not in Ghostty")
			return msg
		}
		if err == nil {
			err = call(locateTimeout, func(ctx context.Context) (e error) { id, e = j.Locate(ctx, tty); return })
		}
		if err == nil {
			err = call(focusTimeout, func(ctx context.Context) error { return j.Focus(ctx, id) })
		}
		if err != nil {
			msg.drop = true
			msg.note = ttyNote(name, tty, jumpReason(err))
			return msg
		}
		msg.id = id
		return msg
	}
}
