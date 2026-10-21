package diffview

import (
	"crypto/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/review"
	"overview/internal/vcs"
)

// Run shows the review until the user closes it. mux is the tmux server
// the viewer and its session run in, nil outside one.
func Run(cfg Config, mux panes) error {
	d := deps{
		run:   vcs.Exec,
		read:  vcs.ReadUntracked,
		stat:  statFile,
		now:   time.Now,
		rnd:   rand.Reader,
		copy:  copyText,
		store: review.Store{Path: cfg.Store},
		find:  func(dir string) (vcs.Repo, bool) { return vcs.Find(dir, exists) },
	}
	if mux != nil {
		d.mux = mux
	}
	m := newModel(cfg, d)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func statFile(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{info.ModTime(), info.Size(), true}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyText(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
