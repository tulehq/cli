// Package cli holds the tule commands.
package cli

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// DefaultStack is the stack name when --stack is not given.
const DefaultStack = "tule"

// globals are the flags every command takes, and the runtime the commands
// that run a stack use.
type globals struct {
	// newRuntime returns the runtime, writing its progress to out.
	newRuntime func(out io.Writer) runtime.Runtime
	stack      string
	configFile string
}

// NewRootCommand returns the tule command with its subcommands, running
// stacks with docker compose.
func NewRootCommand() *cobra.Command {
	return newRootCommand(func(out io.Writer) runtime.Runtime {
		return runtime.NewCompose(out)
	})
}

func newRootCommand(newRuntime func(out io.Writer) runtime.Runtime) *cobra.Command {
	g := &globals{newRuntime: newRuntime}
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
		newUpCommand(g),
		newDownCommand(g),
		newStatusCommand(g),
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
		if err := warn(cmd, w); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// runtime returns the runtime once it has checked it can run.
func (g *globals) runtime(cmd *cobra.Command) (runtime.Runtime, error) {
	rt := g.newRuntime(cmd.ErrOrStderr())
	if err := rt.Check(cmd.Context()); err != nil {
		return nil, err //nolint:wrapcheck // Check names the fix.
	}
	return rt, nil
}

// readState reads the stack back from its containers.
func (g *globals) readState(cmd *cobra.Command, rt runtime.Runtime) (*stack.State, error) {
	st, err := stack.Read(cmd.Context(), rt, g.stack)
	if err != nil {
		return nil, err //nolint:wrapcheck // Says which stack and what failed.
	}
	return st, nil
}

// warnConfigConflict warns of a stack whose containers were created from
// more than one config file, for the commands that work on it anyway.
func warnConfigConflict(cmd *cobra.Command, st *stack.State) error {
	if err := st.ConfigConflict(); err != nil {
		return warn(cmd, err.Error())
	}
	return nil
}

// warn prints a warning to standard error.
func warn(cmd *cobra.Command, msg string) error {
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+msg); err != nil {
		return fmt.Errorf("print a warning: %w", err)
	}
	return nil
}
