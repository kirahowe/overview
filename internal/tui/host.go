package tui

import (
	"os/exec"
	"strconv"
	"strings"

	"overview/internal/fleet"
	"overview/internal/world"
)

// maxHops bounds the walk from a session up to the app that started it, and
// also ends the walk on a cycle.
const maxHops = 12

// psTable lists every process as `pid ppid tty comm`.
func psTable() ([]byte, error) {
	return exec.Command("ps", "-A", "-o", "pid=,ppid=,tty=,comm=").Output()
}

// proc is one line of the process table.
type proc struct {
	ppid int
	tty  string // "" when the process has no controlling terminal
	comm string
}

// parseProcs reads psTable's output. comm is the rest of the line, since an
// executable path may hold spaces. Malformed lines are skipped.
func parseProcs(out string) map[int]proc {
	procs := map[int]proc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		tty := f[2]
		if tty == "??" || tty == "-" {
			tty = ""
		}
		procs[pid] = proc{ppid: ppid, tty: tty, comm: strings.Join(f[3:], " ")}
	}
	return procs
}

// places finds where each interactive session runs. A session with a tty is
// in a terminal, and its tty is how the stage finds its tmux pane. A session
// that is running but has none was started by another app, which is named
// from the nearest ancestor inside an app bundle. A session ps does not list is in
// neither map.
func places(procs map[int]proc, rows []fleet.Row) (ttys map[int]string, hosts map[int]world.Host) {
	ttys, hosts = map[int]string{}, map[int]world.Host{}
	for _, r := range rows {
		if r.Background() || r.PID <= 0 {
			continue
		}
		p, ok := procs[r.PID]
		switch {
		case !ok:
		case p.tty != "":
			ttys[r.PID] = p.tty
		default:
			hosts[r.PID] = hostOf(procs, r.PID)
		}
	}
	return ttys, hosts
}

// hostOf walks up from pid, at most maxHops parents, to the first process
// whose executable sits inside an app bundle. pid itself does not count.
func hostOf(procs map[int]proc, pid int) world.Host {
	p, ok := procs[pid]
	for hop := 0; ok && hop < maxHops && p.ppid > 0; hop++ {
		if p, ok = procs[p.ppid]; ok {
			if h, found := appBundle(p.comm); found {
				return h
			}
		}
	}
	return world.Host{}
}

// appBundle returns the outermost .app in an executable's path. A helper
// process lives in a bundle nested inside its app's, as in
// /Applications/Obsidian.app/Contents/Frameworks/Obsidian Helper.app/...,
// and the outer one is the app the user knows.
func appBundle(comm string) (world.Host, bool) {
	if !strings.HasPrefix(comm, "/") {
		return world.Host{}, false
	}
	i := strings.Index(comm, ".app/")
	if i < 0 {
		return world.Host{}, false
	}
	path := comm[:i+len(".app")]
	name := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".app")
	if name == "" {
		return world.Host{}, false
	}
	return world.Host{Name: name, Path: path}, true
}
