package tui

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

func TestEnsureView(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	srv := tmux.On(fmt.Sprintf("overview-launch-%d", os.Getpid()), "/dev/null")
	t.Cleanup(func() { srv.Run("kill-server") }) //nolint:errcheck
	home := fleet.Home(t.TempDir())
	// The view's stand-in marks its own pane, as Run does.
	view := []string{"sh", "-c", `tmux -L "$1" set-option -p -t "$TMUX_PANE" @overview-list 1; sleep 300`, "sh", srv.Socket()}

	count := func() (list, all int) {
		panes, err := srv.Panes()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range panes {
			if p.Session == "ov" {
				all++
				if p.List {
					list++
				}
			}
		}
		return list, all
	}
	waitForList := func() {
		for range 50 {
			if l, _ := count(); l > 0 {
				return
			}
			exec.Command("sleep", "0.05").Run() //nolint:errcheck
		}
		t.Fatal("the view never marked its pane")
	}

	if err := ensureView(srv, "ov", home, view); err != nil {
		t.Fatal(err)
	}
	waitForList()
	if l, all := count(); l != 1 || all != 1 {
		t.Fatalf("new session: %d list panes of %d", l, all)
	}
	// A view already there is left alone.
	if err := ensureView(srv, "ov", home, view); err != nil {
		t.Fatal(err)
	}
	if l, all := count(); l != 1 || all != 1 {
		t.Fatalf("second launch: %d list panes of %d", l, all)
	}
	// The view quits but its stage lives on: a new view goes beside it.
	panes, _ := srv.Panes()
	stage, err := srv.SplitLeft(panes[0].ID, "70%", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Run("kill-pane", "-t", panes[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := ensureView(srv, "ov", home, view); err != nil {
		t.Fatal(err)
	}
	waitForList()
	if l, all := count(); l != 1 || all != 2 {
		t.Fatalf("relaunch: %d list panes of %d", l, all)
	}
	if _, err := srv.Run("display", "-p", "-t", stage.ID, ""); err != nil {
		t.Fatal("the stage did not survive")
	}
}
