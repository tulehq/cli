package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

func newDownCommand(g *globals) *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down [service...]",
		Short: "Remove services from the stack, or the whole stack",
		Long: "With services named, remove them from the stack: the services wired to them are\n" +
			"recreated without their endpoints, and listed first. With none named, remove the\n" +
			"whole stack. Data is kept in the stack's volumes unless --volumes is given.\n" +
			"Function containers left on the stack's network are removed too.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := g.runtime(cmd)
			if err != nil {
				return err
			}
			st, err := g.readState(cmd, rt)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return g.downStack(cmd, rt, st, volumes)
			}
			return g.downServices(cmd, rt, st, args, volumes)
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also delete the volumes, and the data in them")
	return cmd
}

// downStack removes every container, the network, and with volumes, the
// volumes of every catalog service. It needs no configuration, so it works
// on a stack whose containers disagree on theirs.
func (g *globals) downStack(
	cmd *cobra.Command,
	rt runtime.Runtime,
	st *stack.State,
	volumes bool,
) error {
	if err := warnConfigConflict(cmd, st); err != nil {
		return err
	}
	// They hold the stack's network, which compose removes next.
	if err := removeFunctionContainers(cmd, rt, g.stack); err != nil {
		return err
	}
	if st.Running() {
		err := rt.Down(cmd.Context(), downProject(st), runtime.DownOptions{Volumes: volumes})
		if err != nil {
			return err //nolint:wrapcheck // The runtime says what failed.
		}
	}
	removed := 0
	if volumes {
		var err error
		if removed, err = removeCatalogVolumes(cmd, rt, g.stack); err != nil {
			return err
		}
	}
	switch {
	case st.Running():
		return writeLine(cmd, "stack "+g.stack+" is down")
	case removed > 0:
		return writeLine(
			cmd,
			fmt.Sprintf("stack %s has no containers; removed %d volumes", g.stack, removed),
		)
	default:
		return writeLine(cmd, "stack "+g.stack+" has no containers")
	}
}

// removeCatalogVolumes deletes the stack's volumes of every catalog
// service, those of services removed earlier included, and returns how
// many. Other volumes in the stack's compose project are left alone.
func removeCatalogVolumes(cmd *cobra.Command, rt runtime.Runtime, name string) (int, error) {
	// Read again: compose down has removed the volumes it knew of.
	st, err := stack.Read(cmd.Context(), rt, name)
	if err != nil {
		return 0, err //nolint:wrapcheck // Says which stack and what failed.
	}
	names := st.VolumeNames(stack.CatalogVolumeKeys())
	if err := rt.RemoveVolumes(cmd.Context(), names); err != nil {
		return 0, err //nolint:wrapcheck // The runtime says what failed.
	}
	return len(names), nil
}

// removeFunctionContainers removes the function containers on the stack's
// network, and says how many there were.
func removeFunctionContainers(cmd *cobra.Command, rt runtime.Runtime, name string) error {
	n, err := rt.RemoveFunctionContainers(cmd.Context(), name)
	if err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	if n == 0 {
		return nil
	}
	return writeLine(cmd, fmt.Sprintf("removed %d function containers", n))
}

// downProject is the project compose takes down: the stack's services as
// its containers report them, and their volumes. Compose removes the
// containers of any other service as orphans. The container services are
// catalog names, which the state checked.
func downProject(st *stack.State) *render.Project {
	p := &render.Project{
		Name:     st.Name,
		Services: map[string]render.Service{},
		Volumes:  map[string]render.Volume{},
	}
	for name, c := range st.Containers {
		p.Services[name] = render.Service{
			Image:  strings.ReplaceAll(c.Image, "$", "$$"),
			Labels: map[string]string{},
		}
		if svc := mustLookup(name); svc.Volume != "" {
			p.Volumes[svc.Volume] = render.Volume{}
		}
	}
	return p
}

// errDownKeepsFunctions is returned when removing services would re-render
// a stack that keeps functions, which tule does not render for a stack yet.
var errDownKeepsFunctions = errors.New(
	"the stack runs functions, which tule does not manage yet, so it cannot remove " +
		"other services alone; name functions too, or remove the whole stack with tule down",
)

// downServices removes services from the stack's selection and brings the
// stack to the rest.
func (g *globals) downServices(
	cmd *cobra.Command,
	rt runtime.Runtime,
	st *stack.State,
	names []string,
	volumes bool,
) error {
	rest, err := remaining(st, names)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return g.downStack(cmd, rt, st, volumes)
	}
	if slices.Contains(rest, catalog.Functions) {
		return errDownKeepsFunctions
	}
	sel, err := catalog.NewSelection(rest)
	if errors.Is(err, catalog.ErrConsoleAlone) {
		return fmt.Errorf(
			"removing %s leaves console alone, which needs another service; name console too, or run tule down for the whole stack",
			strings.Join(names, ", "),
		)
	}
	if err != nil {
		return fmt.Errorf("down: %w", err)
	}
	cfg, err := g.loadConfig(cmd)
	if err != nil {
		return err
	}
	if err = st.CheckConfig(cfg.Path); err != nil {
		return err //nolint:wrapcheck // Names the config files.
	}
	if err = g.apply(cmd, rt, cfg, st, sel, true); err != nil {
		return err
	}
	if err = removeLeftovers(cmd, rt, st, names, volumes); err != nil {
		return err
	}
	return writeLine(
		cmd,
		fmt.Sprintf("removed %s from stack %s", strings.Join(names, ", "), g.stack),
	)
}

// remaining returns the stack's services without names, which it must
// have.
func remaining(st *stack.State, names []string) ([]string, error) {
	if !st.Running() {
		return nil, fmt.Errorf("stack %q is not running", st.Name)
	}
	services := st.Services()
	for _, name := range names {
		if _, err := catalog.Lookup(name); err != nil {
			return nil, fmt.Errorf("down: %w", err)
		}
		if !slices.Contains(services, name) {
			return nil, fmt.Errorf("stack %q does not run %s; it runs %s",
				st.Name, name, strings.Join(services, ", "))
		}
	}
	var rest []string
	for _, name := range services {
		if !slices.Contains(names, name) {
			rest = append(rest, name)
		}
	}
	return rest, nil
}

// removeLeftovers removes what compose leaves of services removed from a
// stack: function containers when functions is removed, and with volumes,
// the services' volumes.
func removeLeftovers(
	cmd *cobra.Command,
	rt runtime.Runtime,
	st *stack.State,
	names []string,
	volumes bool,
) error {
	if slices.Contains(names, catalog.Functions) {
		if err := removeFunctionContainers(cmd, rt, st.Name); err != nil {
			return err
		}
	}
	if !volumes {
		return nil
	}
	var keys []string
	for _, name := range names {
		if svc := mustLookup(name); svc.Volume != "" {
			keys = append(keys, svc.Volume)
		}
	}
	if err := rt.RemoveVolumes(cmd.Context(), st.VolumeNames(keys)); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	return nil
}
