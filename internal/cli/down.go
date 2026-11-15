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

// downStack removes every container, the network, and with volumes, every
// volume of the stack.
func (g *globals) downStack(
	cmd *cobra.Command,
	rt runtime.Runtime,
	st *stack.State,
	volumes bool,
) error {
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
		if removed, err = removeAllVolumes(cmd, rt, g.stack); err != nil {
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

// removeAllVolumes deletes every volume of the stack, those of services
// removed earlier included, and returns how many.
func removeAllVolumes(cmd *cobra.Command, rt runtime.Runtime, name string) (int, error) {
	left, err := rt.Volumes(cmd.Context(), name)
	if err != nil {
		return 0, err //nolint:wrapcheck // The runtime says what failed.
	}
	names := make([]string, 0, len(left))
	for _, v := range left {
		names = append(names, v.Name)
	}
	if err := rt.RemoveVolumes(cmd.Context(), names); err != nil {
		return 0, err //nolint:wrapcheck // The runtime says what failed.
	}
	return len(names), nil
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
			p.Volumes[svc.Volume] = render.Volume{Labels: map[string]string{}}
		}
	}
	return p
}

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
		return err //nolint:wrapcheck // Names both config files.
	}
	if err = g.apply(cmd, rt, cfg, st, sel); err != nil {
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

// remaining returns the stack's selection without names, which it must
// hold.
func remaining(st *stack.State, names []string) ([]string, error) {
	if !st.Running() {
		return nil, fmt.Errorf("stack %q is not running", st.Name)
	}
	for _, name := range names {
		if _, err := catalog.Lookup(name); err != nil {
			return nil, fmt.Errorf("down: %w", err)
		}
		if !st.Selection.Has(name) {
			return nil, fmt.Errorf(
				"stack %q does not run %s; it runs %s",
				st.Name,
				name,
				st.Selection,
			)
		}
	}
	var rest []string
	for _, name := range st.Selection.Names() {
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
