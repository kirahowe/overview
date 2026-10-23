// Command overview shows the Claude Code sessions running under one config
// directory and writes a periodic digest of what they did.
package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/lipgloss"

	"overview/internal/config"
	"overview/internal/diffview"
	"overview/internal/fleet"
	"overview/internal/review"
	"overview/internal/tmux"
	"overview/internal/tui"
)

const usage = `overview: watch the Claude Code sessions of one config directory

  overview [flags]            open the live view in overview's tmux, starting
                              it if need be
  overview sessions [flags]   print the sessions once and exit
  overview orchestrator start|stop|status|ensure [flags]
                              run the background session that writes digests;
                              ensure restarts it when it has gone silent
  overview digest [flags]     write one digest now, without the orchestrator
  overview activity [flags]   print what sessions did since the last digest
  overview publish [file]     save a digest (from the file or stdin)
  overview hook               record a Claude Code hook event (reads stdin)
  overview view --socket-path <path> --session <name> --cwd <dir> [flags]
                              the list itself, which overview starts in its
                              tmux with every setting as a flag (internal)
  overview stage              the pane the view draws details into (internal)
  overview themes [name]      list the themes, or print one as a file to edit
  overview review notes|reply|addressed --store <file> ...
                              read and answer the notes of a review (for
                              the agent; see overview review help)
  overview review view --store <file> --dir <dir> [flags]
                              the review itself, which the view starts in
                              a pane above a session (internal)

Flags:
  --home <name|path>   Claude Code config directory; "work" means
                       ~/.claude-work. Defaults to the config file's home line.
  --every <duration>   how often the orchestrator writes a digest (default 1h)
  --model <model>      model for the orchestrator or a one-off digest
                       (default sonnet)
  --theme <name|path>  colours for the live view. Defaults to the config
                       file's theme line, then one-dark.
  --claude <path>      the claude to run. Defaults to the config file's
                       claude line, then claude on PATH.
  --issues <path>      the issues CLI, which adds each project's issues to
                       the view. Defaults to the config file's issues line,
                       then issues on PATH; without one the view is as before.
  --socket <name>      the name of overview's tmux socket. Defaults to the
                       config file's socket line, then overview.
  --config <dir>       overview's own directory, holding the config file,
                       themes and tmux.conf (default ~/.config/overview)
  --orchestrator-dir <dir>
                       where the orchestrator runs (default the orchestrator/
                       beside the bin/ that holds overview)

The config file is <config dir>/config, with key = value lines for home,
theme, socket, claude and issues. A flag wins over the file.
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "hook":
		fleet.Hook(os.Stdin)
		return
	case "stage":
		tui.Stage()
		return
	case "review":
		if err := reviewCmd(args); err != nil {
			fail(err)
		}
		return
	}

	fs := flag.NewFlagSet("overview", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var flags config.Values
	fs.StringVar(&flags.Home, "home", "", "")
	fs.StringVar(&flags.Theme, "theme", "", "")
	fs.StringVar(&flags.Claude, "claude", "", "")
	fs.StringVar(&flags.Issues, "issues", "", "")
	fs.StringVar(&flags.Socket, "socket", "", "")
	// The view is given the socket's path, the session it runs in and
	// where overview was started.
	socketPath := fs.String("socket-path", "", "")
	session := fs.String("session", "", "")
	viewCwd := fs.String("cwd", "", "")
	configDir := fs.String("config", "", "")
	orchDir := fs.String("orchestrator-dir", "", "")
	every := fs.Duration("every", time.Hour, "")
	model := fs.String("model", "sonnet", "")
	// Flags may come before or after a positional argument such as "start".
	var rest []string
	for fs.Parse(args); fs.NArg() > 0; fs.Parse(args) {
		rest, args = append(rest, fs.Arg(0)), fs.Args()[1:]
	}

	// The one place the user's home directory and the working directory
	// are looked up.
	user, err := os.UserHomeDir()
	if err != nil {
		fail(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	var cfg config.Config
	if cmd == "view" {
		// The view reads no file: its command line has every setting,
		// resolved by the launch that started it.
		cfg = config.Config{Dir: *configDir, User: user, Values: flags}
	} else if cfg, err = config.Load(*configDir, flags, user, cwd); err != nil {
		fail(err)
	} else {
		cfg = cfg.FindClaude(exec.LookPath).FindIssues(exec.LookPath)
	}
	if cmd == "themes" {
		if err := themes(cfg, rest); err != nil {
			fail(err)
		}
		return
	}

	home, err := resolveHome(cfg, *orchDir, cwd)
	if err != nil {
		fail(err)
	}
	switch cmd {
	case "":
		// The view gets every setting as a flag, resolved, so it runs with
		// exactly what this launch settled on.
		view := []string{"--config", cfg.Dir, "--home", home.Dir, "--theme", cfg.Theme,
			"--claude", cfg.Claude, "--issues", cfg.Issues, "--orchestrator-dir", home.OrchestratorDir, "--model", *model}
		if _, err = tui.LoadTheme(cfg.Theme, cfg.Dir); err == nil {
			err = tui.Launch(home, cfg.Socket, cfg.Dir, view, cwd)
		}
	case "view":
		var theme tui.Theme
		if theme, err = tui.LoadTheme(cfg.Theme, cfg.Dir); err == nil {
			err = tui.Run(tui.View{Home: home, Model: *model, Theme: theme,
				Socket: *socketPath, Session: *session, User: user, Cwd: *viewCwd, ThemeArg: cfg.Theme, ConfigDir: cfg.Dir, Issues: cfg.Issues})
		}
	case "sessions":
		err = printSessions(home, cfg.User)
	case "digest":
		var d fleet.Digest
		if d, err = home.RunDigest(*model); err == nil {
			if d.Quiet {
				fmt.Printf("No session activity since %s.\n", d.Since.Local().Format("Mon 15:04"))
			} else {
				fmt.Print(d.Text)
			}
		}
	case "activity":
		var text string
		var since time.Time
		var quiet bool
		if text, since, quiet, err = home.Activity(); err == nil {
			if quiet {
				text = fmt.Sprintf("No session activity since %s.\n", since.Local().Format("Mon 15:04"))
			}
			fmt.Print(text)
		}
	case "publish":
		in := os.Stdin
		if len(rest) > 0 {
			if in, err = os.Open(rest[0]); err != nil {
				fail(err)
			}
		}
		var body []byte
		if body, err = io.ReadAll(in); err == nil {
			var d fleet.Digest
			if d, err = home.Publish(string(body)); err == nil {
				fmt.Println("Saved", d.Path)
			}
		}
	case "orchestrator":
		err = orchestrator(home, rest, *every, *model)
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

func orchestrator(home fleet.Home, rest []string, every time.Duration, model string) error {
	action := "status"
	if len(rest) > 0 {
		action = rest[0]
	}
	switch action {
	case "start":
		out, err := home.StartOrchestrator(every, model)
		if err != nil {
			return err
		}
		fmt.Printf("%s\nAttach with: claude attach %s\n", out, fleet.OrchestratorName)
		return nil
	case "stop":
		return home.StopOrchestrator()
	case "ensure":
		out, err := home.EnsureOrchestrator(every, model)
		if err != nil {
			return err
		}
		fmt.Println(time.Now().Format("2006-01-02 15:04"), out)
		return nil
	case "status":
		h, err := home.OrchestratorHealth()
		if err != nil {
			return err
		}
		last := "never"
		if !h.LastCheck.IsZero() {
			last = h.LastCheck.Local().Format("Mon 15:04")
		}
		switch {
		case !h.Running:
			fmt.Println("not running, last looked at the fleet:", last)
		case h.Stale:
			fmt.Println("running but silent, last looked at the fleet:", last)
		default:
			fmt.Printf("running, %s, last looked at the fleet: %s\n", h.Live.Status, last)
		}
		return nil
	}
	return fmt.Errorf("unknown orchestrator action %q", action)
}

// resolveHome finds the config directory cfg names and gives it the claude,
// orchestrator directory and overview command to run with.
func resolveHome(cfg config.Config, orchDir, cwd string) (fleet.Home, error) {
	dir, err := fleet.ResolveHome(cfg.Home, cfg.User)
	if err != nil {
		return fleet.Home{}, err
	}
	switch {
	case orchDir == "":
		orchDir = fleet.DefaultOrchestratorDir()
	case !filepath.IsAbs(orchDir):
		orchDir = filepath.Join(cwd, orchDir)
	}
	exe, err := os.Executable()
	if err != nil {
		return fleet.Home{}, err
	}
	h := fleet.Home{Dir: dir, Claude: cfg.Claude, OrchestratorDir: orchDir, User: cfg.User, Exe: exe}
	if cfg.Dir != config.DefaultDir(cfg.User) {
		h.ConfigDir = cfg.Dir
	}
	return h, nil
}

// themes lists the themes, marking the one in use, or prints the named one
// in full.
func themes(cfg config.Config, rest []string) error {
	dir := cfg.Dir
	if len(rest) > 0 {
		t, err := tui.LoadTheme(rest[0], dir)
		if err != nil {
			return err
		}
		fmt.Print(t.Format())
		return nil
	}
	current := cfg.Theme
	for _, t := range tui.Themes(dir) {
		mark, where := " ", "built in"
		if t.Name == current {
			mark = "*"
		}
		if t.Path != "" {
			where = t.Path
		}
		fmt.Printf("%s %-12s %s\n", mark, t.Name, where)
	}
	fmt.Printf("\nYour themes go in %s.\n", filepath.Join(dir, "themes"))
	return nil
}

func printSessions(home fleet.Home, user string) error {
	rows, err := home.Rows()
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Printf("%-8s %-22s %-28s %6s  %s\n", r.State, r.Name, fleet.ShortDir(r.Cwd, user), tui.Ago(r.Since), tui.Subject(r))
	}
	return nil
}

// reviewCmd runs overview review: the viewer, given every setting as a
// flag by the view that starts it, or one of the agent's commands.
func reviewCmd(args []string) error {
	if len(args) == 0 || args[0] != "view" {
		return review.Command(args, os.Stdin, os.Stdout, time.Now, rand.Reader)
	}
	fs := flag.NewFlagSet("overview review view", flag.ExitOnError)
	store := fs.String("store", "", "")
	dir := fs.String("dir", "", "")
	started := fs.Int64("started", 0, "")
	transcript := fs.String("transcript", "", "")
	socket := fs.String("socket-path", "", "")
	target := fs.String("target", "", "")
	title := fs.String("title", "", "")
	theme := fs.String("theme", config.DefaultTheme, "")
	configDir := fs.String("config", "", "")
	fs.Parse(args[1:])
	if *store == "" || *dir == "" {
		return errors.New("overview review view needs --store and --dir")
	}
	th, err := tui.LoadTheme(*theme, *configDir)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfg := diffview.Config{Store: *store, Dir: *dir, Transcript: *transcript, Target: *target, Title: *title,
		Exe: exe, Theme: th, Profile: lipgloss.ColorProfile()}
	if *started > 0 {
		cfg.Started = time.Unix(*started, 0)
	}
	if *socket == "" {
		return diffview.Run(cfg, nil)
	}
	srv := tmux.At(*socket, "")
	if self, ok, err := srv.Self(os.Getpid()); err == nil && ok {
		cfg.Self = self
	}
	return diffview.Run(cfg, srv)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "overview:", err)
	os.Exit(1)
}
