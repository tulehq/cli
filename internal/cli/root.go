// Package cli holds the tule commands.
package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
)

// DefaultStack is the stack name when --stack is not given.
const DefaultStack = "tule"

// globals are the flags every command takes.
type globals struct {
	stack      string
	configFile string
}

// NewRootCommand returns the tule command with its subcommands.
func NewRootCommand() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:          "tule",
		Short:        "Run a local Tule stack",
		SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return render.ValidateStack(g.stack)
		},
	}
	root.PersistentFlags().
		StringVar(&g.stack, "stack", DefaultStack, "the stack's name, which namespaces its containers, network and volumes")
	root.PersistentFlags().StringVar(&g.configFile, "config", "",
		"the configuration file (default: "+config.FileName+" in the working directory or the nearest parent)")
	root.AddCommand(
		newVersionCommand(debug.ReadBuildInfo),
		newServicesCommand(),
		newComposeCommand(g),
		newEnvCommand(g),
	)
	return root
}

// loadConfig reads the configuration the flags name, or the one found from
// the working directory, and prints its warnings.
func (g *globals) loadConfig(cmd *cobra.Command) (*config.Config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	cfg, warnings, err := config.Load(config.Options{Dir: dir, File: g.configFile})
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for _, w := range warnings {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w); err != nil {
			return nil, fmt.Errorf("print a warning: %w", err)
		}
	}
	return cfg, nil
}
