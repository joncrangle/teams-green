package main

import (
	"runtime/debug"

	"github.com/joncrangle/teams-green/cmd"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	v := version
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
	}
	cmd.SetVersion(v)
	cmd.Execute()
}
