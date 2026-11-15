package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// errFunctionsUnsupported is returned when a stack tule runs would hold
// functions, which needs the Docker socket's group probed first.
var errFunctionsUnsupported = errors.New(
	"tule up does not run functions yet; write its compose file with " +
		"tule compose functions <service...> --docker-gid <gid> and run it with docker compose",
)

func newUpCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "up [service...]",
		Short: "Start the stack, or add services to it",
		Long: "Start the stack and wait until its services are healthy.\n\n" +
			"On a new stack, start the services named, or, with none named, the configured\n" +
			"services or the default set: every service but functions. On a running stack,\n" +
			"add the services named to it, or, with none named, apply the configuration to\n" +
			"the services it runs. Services whose wiring or version changes are listed, then\n" +
			"recreated.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := g.runtime(cmd)
			if err != nil {
				return err
			}
			cfg, err := g.loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := g.readState(cmd, rt)
			if err != nil {
				return err
			}
			if err = st.CheckConfig(cfg.Path); err != nil {
				return err //nolint:wrapcheck // Names both config files.
			}
			sel, err := upSelection(cfg, st, args)
			if err != nil {
				return fmt.Errorf("up: %w", err)
			}
			if err := g.apply(cmd, rt, cfg, st, sel); err != nil {
				return err
			}
			return writeLine(cmd, fmt.Sprintf("stack %s is up: %s", g.stack, sel))
		},
	}
}

// upSelection is the services named, the configured ones or the default set
// on a new stack, and on a running stack, its selection with the services
// named added.
func upSelection(cfg *config.Config, st *stack.State, names []string) (catalog.Selection, error) {
	if !st.Running() {
		return cfg.Selection(names) //nolint:wrapcheck // Says what is wrong with the names.
	}
	if len(names) == 0 {
		return st.Selection, nil
	}
	sel, err := catalog.NewSelection(append(st.Selection.Names(), names...))
	if err != nil {
		return catalog.Selection{}, fmt.Errorf("select services: %w", err)
	}
	return sel, nil
}

// apply renders the selection, prints what it changes in the stack, checks
// the host ports are free, and brings the stack to it.
func (g *globals) apply(
	cmd *cobra.Command,
	rt runtime.Runtime,
	cfg *config.Config,
	st *stack.State,
	sel catalog.Selection,
) error {
	if sel.Has(catalog.Functions) {
		return errFunctionsUnsupported
	}
	project, err := render.Render(render.Input{
		Stack:          g.stack,
		Config:         cfg,
		Selection:      sel,
		VolumeVersions: st.VolumeVersions(),
	})
	if err != nil {
		return fmt.Errorf("render the stack: %w", err)
	}
	bindings, err := stack.Bindings(cfg, sel)
	if err != nil {
		return err //nolint:wrapcheck // Says which ports.
	}
	if err := stack.NewPlan(st, project, bindings).Write(cmd.OutOrStdout()); err != nil {
		return err //nolint:wrapcheck // Says what failed.
	}
	if err := stack.Preflight(cmd.Context(), bindings, st.Held); err != nil {
		return err //nolint:wrapcheck // Names the ports and the keys that move them.
	}
	if err := rt.Up(cmd.Context(), project); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	return nil
}

// writeLine prints a line to standard output.
func writeLine(cmd *cobra.Command, line string) error {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
		return fmt.Errorf("print: %w", err)
	}
	return nil
}
