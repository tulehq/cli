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
	var skipPortCheck bool
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Start the stack, or add services to it",
		Long: "Start the stack and wait until its services are healthy.\n\n" +
			"On a new stack, start the services named, or, with none named, the configured\n" +
			"services or the default set: every service but functions. On a running stack,\n" +
			"add the services named to it, or, with none named, apply the configuration to\n" +
			"the services it runs. Services whose wiring or version changes are listed, then\n" +
			"recreated.\n\n" +
			"Every host port is checked free on this machine first; with a remote Docker\n" +
			"daemon, whose ports this machine cannot see, pass --skip-port-check.",
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
				return err //nolint:wrapcheck // Names the config files.
			}
			sel, err := upSelection(cmd, cfg, st, args)
			if err != nil {
				return fmt.Errorf("up: %w", err)
			}
			if err := g.apply(cmd, rt, cfg, st, sel, !skipPortCheck); err != nil {
				return err
			}
			return writeLine(cmd, fmt.Sprintf("stack %s is up: %s", g.stack, sel))
		},
	}
	cmd.Flags().BoolVar(&skipPortCheck, "skip-port-check", false,
		"do not check the host ports are free on this machine, for a remote Docker daemon")
	return cmd
}

// upSelection is the services named, the configured ones or the default set
// on a new stack, and on a running stack, its services with the services
// named added. A running stack keeps its services over tule.yaml's
// services:, with a warning when they differ.
func upSelection(
	cmd *cobra.Command,
	cfg *config.Config,
	st *stack.State,
	names []string,
) (catalog.Selection, error) {
	if !st.Running() {
		return cfg.Selection(names) //nolint:wrapcheck // Says what is wrong with the names.
	}
	running, err := st.Selection()
	if err != nil {
		return catalog.Selection{}, err //nolint:wrapcheck // Says which stack.
	}
	if len(names) == 0 {
		if err = warnServicesIgnored(cmd, cfg, running); err != nil {
			return catalog.Selection{}, err
		}
		return running, nil
	}
	sel, err := catalog.NewSelection(append(running.Names(), names...))
	if err != nil {
		return catalog.Selection{}, fmt.Errorf("select services: %w", err)
	}
	return sel, nil
}

// warnServicesIgnored warns when tule.yaml's services: differ from the
// running stack's, which a bare up keeps.
func warnServicesIgnored(cmd *cobra.Command, cfg *config.Config, running catalog.Selection) error {
	if cfg.Services == nil {
		return nil
	}
	configured, err := cfg.Selection(nil)
	if err != nil {
		return err //nolint:wrapcheck // Says what is wrong with services:.
	}
	if configured.String() == running.String() {
		return nil
	}
	return warn(cmd, fmt.Sprintf(
		"tule.yaml's services: (%s) differ from the running stack's (%s), which up keeps; "+
			"add services with tule up <service...>, and remove them with tule down <service...>",
		configured, running))
}

// apply renders the selection, prints what it changes in the stack, checks
// the host ports are free, and brings the stack to it.
func (g *globals) apply(
	cmd *cobra.Command,
	rt runtime.Runtime,
	cfg *config.Config,
	st *stack.State,
	sel catalog.Selection,
	checkPorts bool,
) error {
	if sel.Has(catalog.Functions) {
		return errFunctionsUnsupported
	}
	project, err := render.Render(render.Input{
		Stack:     g.stack,
		Config:    cfg,
		Selection: sel,
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
	if checkPorts {
		if err := stack.Preflight(cmd.Context(), bindings, st.Held); err != nil {
			return err //nolint:wrapcheck // Names the ports and the keys that move them.
		}
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
