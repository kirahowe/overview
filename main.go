// Command overview shows the Claude Code sessions running under one config
// directory and writes a periodic digest of what they did.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"overview/internal/fleet"
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
  overview stage              the pane the view draws details into (internal)
  overview themes [name]      list the themes, or print one as a file to edit

Flags:
  --home <name|path>   config directory; "work" means ~/.claude-work.
                       Defaults to CLAUDE_CONFIG_DIR.
  --every <duration>   how often the orchestrator writes a digest (default 1h)
  --model <model>      model for the orchestrator or a one-off digest
                       (default sonnet)
  --theme <name|path>  colours for the live view. Defaults to OVERVIEW_THEME,
                       then the theme line in ~/.config/overview/config,
                       then one-dark.
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
	}

	fs := flag.NewFlagSet("overview", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	homeFlag := fs.String("home", "", "")
	every := fs.Duration("every", time.Hour, "")
	model := fs.String("model", "sonnet", "")
	themeFlag := fs.String("theme", "", "")
	// Flags may come before or after a positional argument such as "start".
	var rest []string
	for fs.Parse(args); fs.NArg() > 0; fs.Parse(args) {
		rest, args = append(rest, fs.Arg(0)), fs.Args()[1:]
	}

	if cmd == "themes" {
		if err := themes(*themeFlag, rest); err != nil {
			fail(err)
		}
		return
	}

	home, err := fleet.ResolveHome(*homeFlag)
	if err != nil {
		fail(err)
	}
	switch cmd {
	case "":
		var theme tui.Theme
		switch theme, err = loadTheme(*themeFlag); {
		case err != nil:
		case tui.InTmux():
			err = tui.Run(home, *model, theme)
		default:
			err = tui.Launch(home, os.Args[1:])
		}
	case "sessions":
		err = printSessions(home)
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

func loadTheme(flag string) (tui.Theme, error) {
	dir, err := tui.ConfigDir()
	if err != nil {
		return tui.Theme{}, err
	}
	name, err := tui.ChooseTheme(flag, dir)
	if err != nil {
		return tui.Theme{}, err
	}
	return tui.LoadTheme(name, dir)
}

// themes lists the themes, marking the one in use, or prints the named one
// in full.
func themes(flag string, rest []string) error {
	dir, err := tui.ConfigDir()
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		t, err := tui.LoadTheme(rest[0], dir)
		if err != nil {
			return err
		}
		fmt.Print(t.Format())
		return nil
	}
	current, err := tui.ChooseTheme(flag, dir)
	if err != nil {
		return err
	}
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

func printSessions(home fleet.Home) error {
	rows, err := home.Rows()
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Printf("%-8s %-22s %-28s %6s  %s\n", r.State, r.Name, fleet.ShortDir(r.Cwd), tui.Ago(r.Since), tui.Subject(r))
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "overview:", err)
	os.Exit(1)
}
