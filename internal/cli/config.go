package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/spf13/cobra"
)

// newConfigCmd builds the `stash-janitor config` subcommand tree. Currently only `init`
// is planned for Phase 1; future commands (validate, show) can be added here.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the stash-janitor config file",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write a default config file (XDG location or --config path)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigInitPath()
			}
			if err := config.EnsureDir(path); err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}
			if err := config.WriteDefault(path); err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; refusing to overwrite (delete or move it first)", path)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote default config to %s\n", path)
			fmt.Fprintf(cmd.OutOrStdout(), "edit stash.url to point at your Stash instance, then run `stash-janitor stats`\n")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file path that would be used",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Load the current config (defaults + your overrides) and print it as YAML",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := flagConfigPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			// Round-trip via the loader's struct so the user sees what's
			// actually in effect, not what's literally in the file. This is
			// the easiest way to debug "is my override being applied?".
			out, err := config.MarshalYAML(cfg)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	})

	return cmd
}
