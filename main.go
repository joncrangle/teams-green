package main

import (
	"github.com/joncrangle/teams-green/cmd"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
