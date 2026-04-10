// Package cli wires up the cobra command tree and global flags for stash-janitor.
package cli

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// Global flag values, populated by cobra during command setup.
var (
	flagConfigPath string
	flagDBPath     string
	flagVerbose    int
)

// NewRootCmd builds the root cobra command and attaches all subcommands.
// It is exported so main and tests can each construct a fresh tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "stash-janitor",
		Short: "stash-space-fixer — find and resolve duplicate scenes in a Stash library",
		Long: `stash-janitor is a CLI for safely deduplicating videos managed by Stash (stashapp/stash).

It addresses two distinct problems:
  - Workflow A: cross-scene duplicates (separate Stash scenes with the same content)
  - Workflow B: within-scene multi-file cleanup (a single scene with multiple attached files)

All destructive actions go through Stash's GraphQL API. The tool never touches the
filesystem directly. The default action is always the safest one: dry-run for apply
commands, tag-only for cross-scene duplicates, report-only for within-scene files.

See PLAN.md for the full design.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			setupLogger(cmd.ErrOrStderr(), flagVerbose)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&flagConfigPath, "config", "config.yaml", "path to config file")
	root.PersistentFlags().StringVar(&flagDBPath, "db", "stash-janitor.sqlite", "path to local sqlite cache")
	root.PersistentFlags().CountVarP(&flagVerbose, "verbose", "v", "increase log verbosity (-v info, -vv debug)")

	root.AddCommand(newConfigCmd())
	root.AddCommand(newScenesCmd())
	root.AddCommand(newFilesCmd())
	root.AddCommand(newOrphansCmd())
	root.AddCommand(newStatsCmd())

	return root
}

// setupLogger installs a slog default handler whose level depends on -v count:
//
//	0 (default) → warn
//	1 (-v)      → info
//	2+ (-vv)    → debug
func setupLogger(w io.Writer, verbose int) {
	level := slog.LevelWarn
	switch {
	case verbose >= 2:
		level = slog.LevelDebug
	case verbose == 1:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))
}

// stub is a placeholder Run for not-yet-implemented commands. It writes a
// clear message to stderr and returns a non-zero error so scripts can detect
// the gap. Subsequent tasks will replace these as commands are filled in.
func stub(name string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		_, _ = io.WriteString(os.Stderr, "stash-janitor: "+name+" is not implemented yet — see PLAN.md TODO\n")
		os.Exit(2)
		return nil
	}
}
