package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/ghostty"
)

// jumpTimeout bounds each call to Ghostty, so a stuck osascript cannot hang
// a jump for long.
const jumpTimeout = 1500 * time.Millisecond

// jumper is the part of ghostty.Client the view needs. Tests fake it.
type jumper interface {
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
	}
	return "jump failed"
}

// jumpCmd focuses the terminal for tty. With a cached id it tries that first
// and, if Ghostty has forgotten it, locates once more. It returns a jumpMsg
// and never touches the model.
func jumpCmd(j jumper, name, key, tty, cached string) tea.Cmd {
	return func() tea.Msg {
		call := func(f func(ctx context.Context) error) error {
			ctx, cancel := context.WithTimeout(context.Background(), jumpTimeout)
			defer cancel()
			return f(ctx)
		}
		msg := jumpMsg{key: key}
		id := cached
		if id != "" {
			err := call(func(ctx context.Context) error { return j.Focus(ctx, id) })
			if err == nil {
				return msg
			}
			if !errors.Is(err, ghostty.ErrStale) {
				msg.note = ttyNote(name, tty, jumpReason(err))
				return msg
			}
			msg.drop = true
		}
		var err error
		err = call(func(ctx context.Context) (e error) { id, e = j.Locate(ctx, tty); return })
		if err == nil {
			err = call(func(ctx context.Context) error { return j.Focus(ctx, id) })
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
