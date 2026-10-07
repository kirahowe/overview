// Command overview shows the Claude Code sessions running under one config
// directory and writes a periodic digest of what they did.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"overview/internal/fleet"
	"overview/internal/tui"
)

const usage = `overview: watch the Claude Code sessions of one config directory

  overview [flags]            open the live view
  overview sessions [flags]   print the sessions once and exit
  overview digest [flags]     summarize activity since the last digest
  overview hook               record a Claude Code hook event (reads stdin)

Flags:
  --home <name|path>   config directory; "kira" means ~/.claude-kira.
                       Defaults to CLAUDE_CONFIG_DIR.
  --every <duration>   how often the live view writes a digest (default 1h,
                       0 to turn off)
  --model <model>      model that writes the digest (default sonnet)
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	if cmd == "hook" {
		fleet.Hook(os.Stdin)
		return
	}

	fs := flag.NewFlagSet("overview", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	homeFlag := fs.String("home", "", "")
	every := fs.Duration("every", time.Hour, "")
	model := fs.String("model", "sonnet", "")
	fs.Parse(args)

	home, err := fleet.ResolveHome(*homeFlag)
	if err != nil {
		fail(err)
	}
	switch cmd {
	case "":
		err = tui.Run(home, *every, *model)
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
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
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
