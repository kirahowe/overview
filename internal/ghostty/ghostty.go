// Package ghostty finds and focuses a stock Ghostty terminal for a tty, and
// opens new tabs.
//
// Stock Ghostty gives a shell no way to learn its own terminal id, so Locate
// works backwards: it writes a unique token to the tty as an OSC 2 title and
// asks Ghostty which terminal's name contains it. Focus then raises that
// terminal through Ghostty's scripting interface. Every external effect sits
// behind a field on Client so tests need neither Ghostty nor a real tty.
package ghostty

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var (
	// ErrNotRunning means Ghostty is not running. Nothing was launched.
	ErrNotRunning = errors.New("ghostty: not running")
	// ErrStale means Ghostty no longer has a terminal with that id.
	ErrStale = errors.New("ghostty: terminal id is stale")
	// ErrNotFound means the title token never showed up in any terminal.
	ErrNotFound = errors.New("ghostty: no terminal showed the title token")
)

const (
	rounds    = 8                             // write-and-query attempts, about 1 s in all
	settle    = 60 * time.Millisecond         // Ghostty shows a new title after 40-60 ms
	roundCost = settle + 150*time.Millisecond // a round: settle, then a find (70-150 ms)
	maxTitle  = 200                           // runes kept when restoring a title
	tokenBits = 8                             // random bytes in a token, 16 hex chars

	// LocateBudget is how long a Locate that runs every round can take.
	// Give Locate at least this much; with less it runs fewer rounds.
	LocateBudget = 200*time.Millisecond + rounds*roundCost
)

// Runner runs a program and returns its stdout. On failure the error carries
// the program's stderr.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Client holds the seams. Use New for the real one.
type Client struct {
	Run   Runner
	Write func(path string, b []byte) error // writes b to a tty device
	Sleep func(ctx context.Context, d time.Duration) error
	Token func() string
}

// New returns a Client wired to osascript and the real filesystem.
func New() Client {
	return Client{Run: execRun, Write: writeTTY, Sleep: sleep, Token: newToken}
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		// A killed process reports "signal: killed", not the context's
		// error. Wrap the context error so callers can tell a timeout.
		if cerr := ctx.Err(); cerr != nil {
			err = fmt.Errorf("%w: %w", cerr, err)
		}
		return out.Bytes(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func writeTTY(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newToken() string {
	b := make([]byte, tokenBits)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return "ov-" + hex.EncodeToString(b)
}

var ttyRE = regexp.MustCompile(`^(?:/dev/)?(ttys[0-9]+)$`)

// TTYPath validates a tty name such as "ttys042" or "/dev/ttys042" and returns
// its device path. It accepts nothing else, so a caller can never make Locate
// write to an arbitrary file.
func TTYPath(tty string) (string, error) {
	m := ttyRE.FindStringSubmatch(tty)
	if m == nil {
		return "", fmt.Errorf("ghostty: invalid tty %q", tty)
	}
	return "/dev/" + m[1], nil
}

var uuidRE = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// ValidID reports whether id is a UUID.
func ValidID(id string) bool { return uuidRE.MatchString(id) }

// SanitizeTitle drops everything that could end or corrupt an OSC string: C0
// and C1 control characters (which include ESC, BEL and ST), DEL, and invalid
// UTF-8. The result is capped at maxTitle runes.
func SanitizeTitle(s string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(s) && n < maxTitle; {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		if (r == utf8.RuneError && w == 1) || r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// OSCTitle returns one OSC 2 frame (ESC ] 2 ; title BEL) for the sanitised title.
func OSCTitle(title string) []byte {
	return []byte("\x1b]2;" + SanitizeTitle(title) + "\x07")
}

// JXA scripts. Dynamic values arrive through argv, never through the source.
const (
	snapshotJS = `function run(argv) {
  var app = Application("Ghostty");
  if (!app.running()) return JSON.stringify({running: false});
  var t = app.terminals, ids = t.id(), names = t.name();
  var m = {};
  for (var i = 0; i < ids.length; i++) m[ids[i]] = names[i];
  return JSON.stringify({running: true, names: m});
}`
	findJS = `function run(argv) {
  var app = Application("Ghostty");
  if (!app.running()) return JSON.stringify(null);
  return JSON.stringify(app.terminals.whose({name: {_contains: argv[0]}}).id());
}`
	focusJS = `function run(argv) {
  var app = Application("Ghostty");
  if (!app.running()) return "not-running";
  app.focus(app.terminals.byId(argv[0]));
  return "ok";
}`
)

// newTabAS opens a tab in the front window, or a window when there is none,
// and focuses it. It is AppleScript because JXA silently drops a surface
// configuration's settings, and the settings go in one record because a
// configuration built from another loses them. argv is the working
// directory, the initial input and one KEY=VALUE environment entry.
const newTabAS = `on run argv
	if application "Ghostty" is not running then return "not-running"
	tell application "Ghostty"
		set cfg to new surface configuration from {initial working directory:item 1 of argv, initial input:item 2 of argv, environment variables:{item 3 of argv}}
		if (count of windows) is 0 then
			set t to selected tab of (new window with configuration cfg)
		else
			set t to new tab in front window with configuration cfg
		end if
		focus focused terminal of t
	end tell
	return "ok"
end run`

func (c Client) jxa(ctx context.Context, script string, args ...string) ([]byte, error) {
	a := append([]string{"-l", "JavaScript", "-e", script}, args...)
	return c.Run(ctx, "osascript", a...)
}

// Locate returns the id of the Ghostty terminal attached to tty. It snapshots
// terminal names, writes a token title to the tty, polls for the terminal that
// shows it (rewriting the token each round, since the program running there
// may retitle over it), then restores the terminal's previous title.
//
// Once a token has reached the tty, every return puts a title back: the
// snapshot's name for the terminal that was found, or an empty title, which
// lets the terminal fall back to its own. The restore is a plain write, so it
// runs even when ctx has expired. Ghostty 1.3.1 ignores the xterm title stack
// (CSI 22 t / CSI 23 t), which is why the snapshot is needed at all.
//
// Locate starts a round only if ctx leaves time to finish it, so running out
// of rounds or of time both end in ErrNotFound.
func (c Client) Locate(ctx context.Context, tty string) (string, error) {
	path, err := TTYPath(tty)
	if err != nil {
		return "", err
	}
	out, err := c.jxa(ctx, snapshotJS)
	if err != nil {
		return "", fmt.Errorf("ghostty: snapshot: %w", err)
	}
	var snap struct {
		Running bool              `json:"running"`
		Names   map[string]string `json:"names"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &snap); err != nil {
		return "", fmt.Errorf("ghostty: snapshot: %w", err)
	}
	if !snap.Running {
		return "", ErrNotRunning
	}

	token := c.Token()
	frame := OSCTitle(token)
	dirty, restore := false, ""
	defer func() {
		if dirty {
			_ = c.Write(path, OSCTitle(restore)) // best effort
		}
	}()
	n := 0
	for ; n < rounds && roomFor(ctx, roundCost); n++ {
		if err := c.Write(path, frame); err != nil {
			return "", fmt.Errorf("ghostty: write title: %w", err)
		}
		dirty = true
		if err := c.Sleep(ctx, settle); err != nil {
			return "", err
		}
		out, err := c.jxa(ctx, findJS, token)
		if err != nil {
			return "", fmt.Errorf("ghostty: find: %w", err)
		}
		out = bytes.TrimSpace(out)
		if string(out) == "null" {
			return "", ErrNotRunning // Ghostty quit meanwhile
		}
		var ids []string
		if err := json.Unmarshal(out, &ids); err != nil {
			return "", fmt.Errorf("ghostty: find: %w", err)
		}
		switch len(ids) {
		case 0:
			continue
		case 1:
			// A name holding a token belongs to a Locate still in flight
			// (another overview, say); putting it back would pin it.
			if prev := snap.Names[ids[0]]; !tokenRE.MatchString(prev) {
				restore = prev
			}
			return ids[0], nil
		default:
			return "", fmt.Errorf("ghostty: token matched %d terminals", len(ids))
		}
	}
	return "", fmt.Errorf("%w (tty %s, %d rounds)", ErrNotFound, tty, n)
}

// roomFor reports whether ctx leaves at least d before its deadline.
func roomFor(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= d
}

var tokenRE = regexp.MustCompile(`ov-[0-9a-f]{16}`)

// Focus selects the tab holding terminal id, focuses it and raises its window.
// It returns ErrStale if Ghostty has no such terminal and ErrNotRunning if
// Ghostty is not running (it never launches Ghostty).
func (c Client) Focus(ctx context.Context, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("ghostty: invalid terminal id %q", id)
	}
	out, err := c.jxa(ctx, focusJS, id)
	if err != nil {
		if strings.Contains(err.Error(), "-1728") {
			return ErrStale
		}
		return fmt.Errorf("ghostty: focus: %w", err)
	}
	if strings.TrimSpace(string(out)) == "not-running" {
		return ErrNotRunning
	}
	return nil
}

// NewTab opens a Ghostty tab whose shell starts in dir with env (one
// KEY=VALUE entry) set, types input into it if input is not empty, and
// focuses it. It returns ErrNotRunning if Ghostty is not running (it never
// launches Ghostty).
func (c Client) NewTab(ctx context.Context, dir, input, env string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("ghostty: working directory %q is not absolute", dir)
	}
	if !strings.Contains(env, "=") {
		return fmt.Errorf("ghostty: environment entry %q is not KEY=VALUE", env)
	}
	out, err := c.Run(ctx, "osascript", "-e", newTabAS, dir, input, env)
	if err != nil {
		return fmt.Errorf("ghostty: new tab: %w", err)
	}
	if strings.TrimSpace(string(out)) == "not-running" {
		return ErrNotRunning
	}
	return nil
}
