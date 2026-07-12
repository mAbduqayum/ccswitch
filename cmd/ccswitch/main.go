package main

import (
	"fmt"
	"runtime/debug"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = ""

func main() {
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
