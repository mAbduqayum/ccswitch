package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = ""

func main() {
	// Temporary stub until the cobra CLI lands: fail loudly on any
	// arguments so pipelines (completions generation, Nix postInstall)
	// can't silently package garbage.
	if len(os.Args) > 1 {
		fmt.Fprintf(os.Stderr, "ccswitch: CLI not implemented yet (got %q)\n", os.Args[1:])
		os.Exit(1)
	}
	fmt.Println("ccswitch", versionString())
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
