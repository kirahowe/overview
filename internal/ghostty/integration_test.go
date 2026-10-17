package ghostty

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in: set OVERVIEW_GHOSTTY_IT=1. Opens one Ghostty window of its
// own, always closes it, and never touches other windows.
func TestIntegrationReal(t *testing.T) {
	if os.Getenv("OVERVIEW_GHOSTTY_IT") != "1" {
		t.Skip("set OVERVIEW_GHOSTTY_IT=1 to run against a real Ghostty")
	}
	dir := t.TempDir()
	ttyFile := filepath.Join(dir, "tty")
	cmd := "tty > " + ttyFile + "; sleep 600"

	const open = `on run argv
  tell application "Ghostty"
    set cfg to new surface configuration
    set command of cfg to "/bin/sh -c '" & item 1 of argv & "'"
    set w to new window with configuration cfg
    return (id of w) & "|" & (id of terminal 1 of selected tab of w)
  end tell
end run`
	out, err := exec.Command("osascript", "-e", open, cmd).Output()
	if err != nil {
		t.Fatalf("open window: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 2 {
		t.Fatalf("open window output %q", out)
	}
	winID, termID := parts[0], parts[1]
	t.Cleanup(func() {
		const closeW = `on run argv
  tell application "Ghostty"
    set w to window id (item 1 of argv)
    close window w
  end tell
end run`
		if out, err := exec.Command("osascript", "-e", closeW, winID).CombinedOutput(); err != nil {
			t.Errorf("close window %s: %v: %s", winID, err, out)
		}
	})

	var tty string
	for i := 0; i < 50 && tty == ""; i++ {
		time.Sleep(100 * time.Millisecond)
		if b, err := os.ReadFile(ttyFile); err == nil {
			tty = strings.TrimPrefix(strings.TrimSpace(string(b)), "/dev/")
		}
	}
	if tty == "" {
		t.Fatal("window never reported its tty")
	}

	c := New()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	id, err := c.Locate(ctx, tty)
	t.Logf("Locate(%s) = %s in %v", tty, id, time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(id, termID) {
		t.Fatalf("Locate = %s, want %s", id, termID)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel2()
	start = time.Now()
	if err := c.Focus(ctx2, id); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	t.Logf("Focus in %v", time.Since(start))

	ctx3, cancel3 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel3()
	if err := c.Focus(ctx3, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrStale) {
		t.Fatalf("Focus(random) = %v, want ErrStale", err)
	}
}
