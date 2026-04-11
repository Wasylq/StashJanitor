// Command stash-janitor is the StashJanitor CLI: a tool for finding and resolving
// duplicate video scenes in a Stash library.
//
// See PLAN.md at the repository root for design and roadmap.
package main

import (
	"os"

	"github.com/Wasylq/StashJanitor/internal/cli"
)

// Set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/stash-janitor
//
// The release workflow does this automatically from the git tag.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := cli.NewRootCmd(version, commit, date).Execute(); err != nil {
		os.Exit(1)
	}
}
