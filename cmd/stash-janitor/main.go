// Command stash-janitor is the StashJanitor CLI: a tool for finding and resolving
// duplicate video scenes in a Stash library.
//
// See PLAN.md at the repository root for design and roadmap.
package main

import (
	"os"

	"github.com/Wasylq/StashJanitor/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		// Cobra has already printed the error message; just exit non-zero.
		os.Exit(1)
	}
}
