package ghostty

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// maxHops bounds the walk up the process tree. A session sits three or four
// levels below Ghostty (ghostty, login, shell, claude), so twelve is plenty,
// and the bound also ends the walk on a cycle.
const maxHops = 12

// proc is one line of the process table.
type proc struct {
	ppid int
	comm string
}

// InGhostty reports whether the process pid runs under Ghostty, that is,
// whether one of its ancestors is Ghostty itself. Locate writes to a tty, so
// the view asks this first: a session in tmux, Terminal.app or iTerm2 has a
// tty too, and writing a title there would wipe that terminal's own.
func (c Client) InGhostty(ctx context.Context, pid int) (bool, error) {
	out, err := c.Run(ctx, "ps", "-A", "-o", "pid=,ppid=,comm=")
	if err != nil {
		return false, fmt.Errorf("ghostty: ps: %w", err)
	}
	return underGhostty(parsePS(string(out)), pid), nil
}

// parsePS reads `ps -o pid=,ppid=,comm=` output. comm is the rest of the
// line, since an executable path may hold spaces. Malformed lines are skipped.
func parsePS(out string) map[int]proc {
	procs := map[int]proc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs[pid] = proc{ppid: ppid, comm: strings.Join(f[2:], " ")}
	}
	return procs
}

// isGhostty matches Ghostty's main executable by name, as ps shows it on
// macOS: /Applications/Ghostty.app/Contents/MacOS/ghostty.
func isGhostty(comm string) bool {
	return strings.EqualFold(filepath.Base(comm), "ghostty")
}

// underGhostty walks up from pid, at most maxHops parents, and reports
// whether it meets Ghostty. pid itself does not count. The walk stops at a
// missing parent or at pid 0.
func underGhostty(procs map[int]proc, pid int) bool {
	p, ok := procs[pid]
	for hop := 0; ok && hop < maxHops && p.ppid > 0; hop++ {
		if p, ok = procs[p.ppid]; ok && isGhostty(p.comm) {
			return true
		}
	}
	return false
}
