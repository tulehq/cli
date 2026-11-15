// Package cli holds the tule commands.
package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// NewRootCommand returns the tule command with its subcommands.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "tule",
		Short:        "Run a local Tule stack",
		SilenceUsage: true,
	}
	root.AddCommand(newVersionCommand(debug.ReadBuildInfo))
	return root
}
