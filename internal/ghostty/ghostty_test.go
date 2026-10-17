package ghostty

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

type fake struct {
	calls   []call
	writes  [][2]string // path, payload
	snap    string
	finds   []string // answers for successive find calls
	findErr error
	focus   func() ([]byte, error)
}

func (f *fake) client() Client {
	return Client{
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			f.calls = append(f.calls, call{name, args})
			script := args[3]
			switch {
			case script == snapshotJS:
				return []byte(f.snap), nil
			case script == findJS:
				if f.findErr != nil {
					return nil, f.findErr
				}
				a := "[]"
				if len(f.finds) > 0 {
					a, f.finds = f.finds[0], f.finds[1:]
				}
				return []byte(a), nil
			case script == focusJS:
				return f.focus()
			}
			return nil, errors.New("unexpected script")
		},
		Write: func(path string, b []byte) error {
			f.writes = append(f.writes, [2]string{path, string(b)})
			return nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error { return ctx.Err() },
		Token: func() string { return "ov-TOKEN" },
	}
}

const id1 = "D29572A4-1C61-4D8A-A92E-5BE8F251E2F3"

func TestLocateFindsOnThirdRoundAndRestores(t *testing.T) {
	f := &fake{
		snap:  `{"running":true,"names":{"` + id1 + `":"my \u001b title"}}`,
		finds: []string{"[]", "[]", `["` + id1 + `"]`},
	}
	id, err := f.client().Locate(context.Background(), "/dev/ttys042")
	if err != nil || id != id1 {
		t.Fatalf("got %q, %v", id, err)
	}
	// Three token writes (one per round) then the restore.
	if len(f.writes) != 4 {
		t.Fatalf("writes: %q", f.writes)
	}
	for i := 0; i < 3; i++ {
		if f.writes[i] != [2]string{"/dev/ttys042", "\x1b]2;ov-TOKEN\x07"} {
			t.Errorf("write %d = %q", i, f.writes[i])
		}
	}
	if f.writes[3][1] != "\x1b]2;my  title\x07" {
		t.Errorf("restore = %q", f.writes[3][1])
	}
	last := f.calls[len(f.calls)-1].args
	if last[len(last)-1] != "ov-TOKEN" || strings.Contains(last[3], "ov-TOKEN") {
		t.Errorf("token must travel via argv only: %q", last)
	}
}

func TestLocateGivesUp(t *testing.T) {
	f := &fake{snap: `{"running":true,"names":{}}`}
	_, err := f.client().Locate(context.Background(), "ttys001")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "ttys001") {
		t.Fatalf("err = %v", err)
	}
	if len(f.writes) != rounds+1 {
		t.Fatalf("writes = %d, want %d", len(f.writes), rounds+1)
	}
	if last := f.writes[rounds]; last != [2]string{"/dev/ttys001", "\x1b]2;\x07"} {
		t.Errorf("final write = %q, want an empty title", last)
	}
}

func TestLocateNotRunningWritesNothing(t *testing.T) {
	f := &fake{snap: `{"running":false}`}
	_, err := f.client().Locate(context.Background(), "ttys001")
	if !errors.Is(err, ErrNotRunning) || len(f.writes) != 0 {
		t.Fatalf("err = %v, writes = %q", err, f.writes)
	}
}

func TestLocateRejectsBadTTYs(t *testing.T) {
	for _, tty := range []string{"", "tty", "ttys", "ttys1x", "/dev/null", "/dev/ttys1/../null", "../ttys1", "/etc/passwd", "ttys1\n", "/dev/tty", "console"} {
		f := &fake{snap: `{"running":true,"names":{}}`}
		if _, err := f.client().Locate(context.Background(), tty); err == nil {
			t.Errorf("%q accepted", tty)
		}
		if len(f.writes) != 0 || len(f.calls) != 0 {
			t.Errorf("%q: side effects: %v %v", tty, f.writes, f.calls)
		}
	}
}

func TestLocateAmbiguousAndWriteError(t *testing.T) {
	f := &fake{snap: `{"running":true,"names":{}}`, finds: []string{`["a","b"]`}}
	if _, err := f.client().Locate(context.Background(), "ttys1"); err == nil || !strings.Contains(err.Error(), "2 terminals") {
		t.Errorf("err = %v", err)
	}
	if last := f.writes[len(f.writes)-1][1]; last != "\x1b]2;\x07" {
		t.Errorf("ambiguous match left %q as the last write", last)
	}
	f = &fake{snap: `{"running":true,"names":{}}`}
	c := f.client()
	c.Write = func(string, []byte) error { return fmt.Errorf("EIO") }
	if _, err := c.Locate(context.Background(), "ttys1"); err == nil || !strings.Contains(err.Error(), "EIO") {
		t.Errorf("err = %v", err)
	}
}

func TestLocateFindAnswers(t *testing.T) {
	f := &fake{snap: `{"running":true,"names":{}}`, finds: []string{"null"}}
	if _, err := f.client().Locate(context.Background(), "ttys1"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("null answer: err = %v", err)
	}
	f = &fake{snap: `{"running":true,"names":{}}`, finds: []string{"<html>"}}
	if _, err := f.client().Locate(context.Background(), "ttys1"); err == nil || errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "find") {
		t.Errorf("garbage answer: err = %v", err)
	}
}

// A previous name that holds a token is another Locate's leftover. Putting
// it back would pin it, so the title is reset instead.
func TestLocateDoesNotRestoreAToken(t *testing.T) {
	f := &fake{
		snap:  `{"running":true,"names":{"` + id1 + `":"ov-0123456789abcdef"}}`,
		finds: []string{`["` + id1 + `"]`},
	}
	if _, err := f.client().Locate(context.Background(), "ttys1"); err != nil {
		t.Fatal(err)
	}
	if last := f.writes[len(f.writes)-1][1]; last != "\x1b]2;\x07" {
		t.Errorf("restored %q", last)
	}
}

// With a deadline too close for every round, Locate runs the rounds that fit
// and reports ErrNotFound rather than a context error.
func TestLocateFitsRoundsToTheDeadline(t *testing.T) {
	f := &fake{snap: `{"running":true,"names":{}}`}
	c := f.client()
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		time.Sleep(100 * time.Millisecond)
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*roundCost)
	defer cancel()
	_, err := c.Locate(ctx, "ttys1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := len(f.writes) - 1; n < 1 || n >= rounds {
		t.Fatalf("%d rounds ran", n)
	}
	if last := f.writes[len(f.writes)-1][1]; last != "\x1b]2;\x07" {
		t.Errorf("last write %q", last)
	}
}

func TestLocateHonoursContext(t *testing.T) {
	f := &fake{snap: `{"running":true,"names":{}}`}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client().Locate(ctx, "ttys1"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if last := f.writes[len(f.writes)-1][1]; last != "\x1b]2;\x07" {
		t.Errorf("cancelled Locate left %q as the last write", last)
	}
}

func TestFocus(t *testing.T) {
	f := &fake{}
	f.focus = func() ([]byte, error) { return []byte("ok\n"), nil }
	if err := f.client().Focus(context.Background(), id1); err != nil {
		t.Fatal(err)
	}
	if a := f.calls[0].args; a[len(a)-1] != id1 || strings.Contains(a[3], id1) {
		t.Errorf("id must travel via argv only: %q", a)
	}

	f.focus = func() ([]byte, error) {
		return nil, errors.New("osascript: exit status 1: execution error: Error: Can't get object. (-1728)")
	}
	if err := f.client().Focus(context.Background(), id1); !errors.Is(err, ErrStale) {
		t.Errorf("err = %v", err)
	}

	f.focus = func() ([]byte, error) { return []byte("not-running\n"), nil }
	if err := f.client().Focus(context.Background(), id1); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v", err)
	}

	f.focus = func() ([]byte, error) { return nil, errors.New("boom") }
	err := f.client().Focus(context.Background(), id1)
	if err == nil || errors.Is(err, ErrStale) {
		t.Errorf("err = %v", err)
	}
}

func TestFocusRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "x", id1 + "\"", `" & quit`, id1[:35], "ZZ572A4-1C61-4D8A-A92E-5BE8F251E2F3"} {
		f := &fake{}
		if err := f.client().Focus(context.Background(), id); err == nil || len(f.calls) != 0 {
			t.Errorf("%q: err=%v calls=%d", id, err, len(f.calls))
		}
	}
}

// Scripts must never launch Ghostty: every script checks running() before
// anything that would send an Apple event, and none is a `tell application`.
func TestScriptsGuardLaunch(t *testing.T) {
	for _, s := range []string{snapshotJS, findJS, focusJS} {
		if strings.Contains(s, "tell application") {
			t.Errorf("script launches via tell: %s", s)
		}
		if strings.Index(s, "running()") > strings.Index(s, "app.terminals") {
			t.Errorf("running() check must precede terminals access: %s", s)
		}
	}
}
