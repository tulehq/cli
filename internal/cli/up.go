package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

func newUpCommand(g *globals) *cobra.Command {
	var (
		skipPortCheck, allowDowngrade bool
		dockerGID                     string
	)
	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Start the stack, or add services to it",
		Long: "Start the stack and wait until its services are healthy.\n\n" +
			"On a new stack, start the services named, or, with none named, the configured\n" +
			"services or the default set: every service but functions. On a running stack,\n" +
			"add the services named to it, or, with none named, apply the configuration to\n" +
			"the services it runs. Services whose wiring or version changes are listed, then\n" +
			"recreated. Taking a running service down a version needs --allow-downgrade.\n\n" +
			"Every host port is checked free on this machine first; with a remote Docker\n" +
			"daemon, whose ports this machine cannot see, pass --skip-port-check.\n\n" +
			"functions is given the group that owns the Docker socket: the one a running\n" +
			"functions container has, or else the one a throwaway container from the\n" +
			"functions image finds on the daemon; --docker-gid sets it instead.",
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
			if err = checkGIDFlag(cmd, dockerGID); err != nil {
				return err
			}
			opts := applyOptions{
				checkPorts:     !skipPortCheck,
				allowDowngrade: allowDowngrade,
				dockerGID:      dockerGID,
			}
			if err := g.apply(cmd, rt, cfg, st, sel, opts); err != nil {
				return err
			}
			return writeLine(cmd, fmt.Sprintf("stack %s is up: %s", g.stack, sel))
		},
	}
	addSkipPortCheck(cmd, &skipPortCheck)
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false,
		"take running services down a version, which may serve stale data")
	cmd.Flags().StringVar(&dockerGID, "docker-gid", "",
		"the group ID that owns the Docker socket on the daemon's host, given to functions (default: detected)")
	return cmd
}

func addSkipPortCheck(cmd *cobra.Command, skip *bool) {
	cmd.Flags().BoolVar(skip, "skip-port-check", false,
		"do not check the host ports are free on this machine, for a remote Docker daemon")
}

// checkGIDFlag validates --docker-gid when it is given.
func checkGIDFlag(cmd *cobra.Command, gid string) error {
	if !cmd.Flags().Changed("docker-gid") {
		return nil
	}
	if err := render.ValidateGroupID(gid); err != nil {
		return fmt.Errorf("--docker-gid: %w", err)
	}
	return nil
}

// upSelection is the services named, the configured ones or the default set
// on a new stack, and on a running stack, its services with the services
// named added. A running stack keeps its services over tule.yaml's
// services:, with a warning when they differ. The running services alone
// need not be a selection: a stack left with the console alone takes
// services added to it.
func upSelection(
	cmd *cobra.Command,
	cfg *config.Config,
	st *stack.State,
	names []string,
) (catalog.Selection, error) {
	if !st.Running() {
		return cfg.Selection(names) //nolint:wrapcheck // Says what is wrong with the names.
	}
	if len(names) == 0 {
		running, err := st.Selection()
		if err != nil {
			return catalog.Selection{}, err //nolint:wrapcheck // Says which stack.
		}
		if err = warnServicesIgnored(cmd, cfg, running); err != nil {
			return catalog.Selection{}, err
		}
		return running, nil
	}
	sel, err := catalog.NewSelection(append(st.Services(), names...))
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

// applyOptions configures apply.
type applyOptions struct {
	// dockerGID is --docker-gid; empty when not given.
	dockerGID      string
	checkPorts     bool
	allowDowngrade bool
}

// apply renders the selection, prints what it changes in the stack, checks
// no running service goes down a version and the host ports are free, and
// brings the stack to it. The Docker socket's group is probed, which runs a
// container, only once those checks pass.
func (g *globals) apply(
	cmd *cobra.Command,
	rt runtime.Runtime,
	cfg *config.Config,
	st *stack.State,
	sel catalog.Selection,
	opts applyOptions,
) error {
	docker, probe := knownDocker(st, sel, opts.dockerGID)
	planned := docker
	if probe {
		// functions is added, so the plan does not compare its group.
		planned.GroupID = plannedGroup
	}
	project, err := g.render(cfg, sel, planned)
	if err != nil {
		return err
	}
	bindings, err := stack.Bindings(cfg, sel)
	if err != nil {
		return err //nolint:wrapcheck // Says which ports.
	}
	plan := stack.NewPlan(st, project, bindings)
	if err = plan.Write(cmd.OutOrStdout()); err != nil {
		return err //nolint:wrapcheck // Says what failed.
	}
	if err = checkPlan(cmd, plan, bindings, st, opts); err != nil {
		return err
	}
	if probe {
		if docker.GroupID, err = probeGroup(cmd, rt, cfg); err != nil {
			return err
		}
		if project, err = g.render(cfg, sel, docker); err != nil {
			return err
		}
	}
	if err := rt.Up(cmd.Context(), project); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	return nil
}

// plannedGroup stands in for the socket's group until it is probed.
const plannedGroup = "0"

// checkPlan refuses a downgrade unless allowed, and host ports in use
// unless the check is skipped.
func checkPlan(
	cmd *cobra.Command,
	plan stack.Plan,
	bindings []stack.Binding,
	st *stack.State,
	opts applyOptions,
) error {
	if !opts.allowDowngrade {
		if err := plan.CheckDowngrades(); err != nil {
			return fmt.Errorf("%w; run tule up --allow-downgrade to go ahead", err)
		}
	}
	if opts.checkPorts {
		if err := stack.Preflight(cmd.Context(), bindings, st.Held); err != nil {
			return err //nolint:wrapcheck // Names the ports and the keys that move them.
		}
	}
	return nil
}

// render renders the selection with docker for functions.
func (g *globals) render(
	cfg *config.Config,
	sel catalog.Selection,
	docker render.Docker,
) (*render.Project, error) {
	project, err := render.Render(render.Input{
		Stack:     g.stack,
		Config:    cfg,
		Selection: sel,
		Docker:    docker,
	})
	if err != nil {
		return nil, fmt.Errorf("render the stack: %w", err)
	}
	return project, nil
}

// knownDocker is how functions reaches the Docker daemon, when it is
// selected: the socket, and its group from --docker-gid, else from the
// running functions container, so a re-render keeps the group it was
// started with. probe is true when neither gives the group.
func knownDocker(
	st *stack.State,
	sel catalog.Selection,
	flagGID string,
) (docker render.Docker, probe bool) {
	docker = render.Docker{SocketPath: dockerSocket, GroupID: flagGID}
	if !sel.Has(catalog.Functions) || flagGID != "" {
		return docker, false
	}
	if c, ok := st.Containers[catalog.Functions]; ok && len(c.GroupAdd) == 1 {
		docker.GroupID = c.GroupAdd[0]
		return docker, false
	}
	return docker, true
}

// docker is knownDocker, with the group probed when it is not known.
func (g *globals) docker(
	cmd *cobra.Command,
	rt runtime.Runtime,
	cfg *config.Config,
	st *stack.State,
	sel catalog.Selection,
) (render.Docker, error) {
	docker, probe := knownDocker(st, sel, "")
	if !probe {
		return docker, nil
	}
	gid, err := probeGroup(cmd, rt, cfg)
	if err != nil {
		return render.Docker{}, err
	}
	docker.GroupID = gid
	return docker, nil
}

// probeGroup finds the socket's group on the daemon from the functions
// image.
func probeGroup(cmd *cobra.Command, rt runtime.Runtime, cfg *config.Config) (string, error) {
	image := functionsService.ImageRef(cfg.Version(functionsService))
	gid, err := rt.SocketGroup(cmd.Context(), image, dockerSocket)
	if err != nil {
		return "", fmt.Errorf("%w; pass --docker-gid to set it", err)
	}
	return gid, nil
}

// writeLine prints a line to standard output.
func writeLine(cmd *cobra.Command, line string) error {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
		return fmt.Errorf("print: %w", err)
	}
	return nil
}
