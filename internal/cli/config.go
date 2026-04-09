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
		Short: "Write a default config file to --config (default: ./config.yaml)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.WriteDefault(flagConfigPath); err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; refusing to overwrite (delete or move it first)", flagConfigPath)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote default config to %s\n", flagConfigPath)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Load the current config (defaults + your overrides) and print it as YAML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(flagConfigPath)
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
