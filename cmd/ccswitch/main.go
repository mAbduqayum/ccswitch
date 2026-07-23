package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"golang.org/x/term"

	"github.com/mAbduqayum/ccswitch/internal/app"
	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/cli"
	"github.com/mAbduqayum/ccswitch/internal/tui"
	"github.com/mAbduqayum/ccswitch/internal/update"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = ""

func main() {
	env, err := claude.RealEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccswitch:", err)
		os.Exit(1)
	}
	opts := cli.Options{
		Version: versionString(),
		App:     app.New(env),
		RunTUI:  tui.Run,
		Update: &update.Client{
			Releaser: update.NewHTTPReleaser(),
			GOOS:     runtime.GOOS,
			GOARCH:   runtime.GOARCH,
			HomeDir:  env.Home,
			PathEnv:  os.Getenv("PATH"),
		},
		IO: cli.IO{
			In:  os.Stdin,
			Out: os.Stdout,
			Err: os.Stderr,
			// Prompts and the TUI need both directions to be a terminal.
			IsTTY: isTTY(os.Stdin) && isTTY(os.Stdout),
		},
	}
	os.Exit(cli.Execute(opts, os.Args[1:]))
}

// isTTY asks the terminal driver instead of the char-device heuristic,
// which would misclassify /dev/null and fire prompts in cron jobs.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				rev := s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return rev
			}
		}
	}
	return "dev"
}
