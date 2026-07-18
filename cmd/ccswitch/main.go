package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/mAbduqayum/ccswitch/internal/app"
	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/cli"
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

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
