package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// errResetNotConfirmed is returned when the prompt is answered with
// anything but yes.
var errResetNotConfirmed = errors.New("reset not confirmed; nothing was changed")

func newResetCommand(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset <service...>",
		Short: "Delete a service's data: recreate it with an empty volume",
		Long: "Stop the services named, delete their volumes, and start them again with empty\n" +
			"ones. Asks first; --yes skips the question, and is required when standard input\n" +
			"is not a terminal. The other services keep running and keep their data.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.reset(cmd, args, yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before deleting the data")
	return cmd
}

// reset renders the stack, asks unless yes, and recreates the services
// named with empty volumes.
func (g *globals) reset(cmd *cobra.Command, args []string, yes bool) error {
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
	if err = checkRunning(st, args); err != nil {
		return err
	}
	names, keys, err := resetVolumes(args)
	if err != nil {
		return err
	}
	project, err := g.renderRunning(cmd, rt, cfg, st)
	if err != nil {
		return err
	}
	volumes := st.VolumeNames(keys)
	if !yes {
		if err = g.confirmReset(cmd, names, volumes); err != nil {
			return err
		}
	}
	if err = g.recreateEmpty(cmd, rt, project, names, volumes); err != nil {
		return err
	}
	return writeLine(cmd, fmt.Sprintf("reset %s in stack %s: started with empty volumes",
		strings.Join(names, ", "), g.stack))
}

// renderRunning renders the running stack's services from cfg, which must
// be the file it was started from.
func (g *globals) renderRunning(
	cmd *cobra.Command,
	rt runtime.Runtime,
	cfg *config.Config,
	st *stack.State,
) (*render.Project, error) {
	if err := st.CheckConfig(cfg.Path); err != nil {
		return nil, err //nolint:wrapcheck // Names the config files.
	}
	sel, err := st.Selection()
	if err != nil {
		return nil, err //nolint:wrapcheck // Says which stack.
	}
	docker, err := g.docker(cmd, rt, cfg, st, sel)
	if err != nil {
		return nil, err
	}
	return g.render(cfg, sel, docker)
}

// recreateEmpty removes the services' containers, and functions' function
// containers with it, deletes their volumes, and starts them again.
func (g *globals) recreateEmpty(
	cmd *cobra.Command,
	rt runtime.Runtime,
	project *render.Project,
	names, volumes []string,
) error {
	ctx := cmd.Context()
	if err := rt.RemoveServices(ctx, project, names); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	if slices.Contains(names, catalog.Functions) {
		// Their functions are deleted with functions' data.
		if err := removeFunctionContainers(cmd, rt, g.stack); err != nil {
			return err
		}
	}
	if err := rt.RemoveVolumes(ctx, volumes); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	if err := rt.UpServices(ctx, project, names); err != nil {
		return err //nolint:wrapcheck // The runtime says what failed.
	}
	return nil
}

// readAnswer reads a line from standard input, or stops when the command's
// context is done, as on Ctrl-C. The read is left blocked then; tule exits
// right after.
func readAnswer(cmd *cobra.Command) (string, error) {
	type line struct {
		err  error
		text string
	}
	read := make(chan line, 1)
	go func() {
		text, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		read <- line{text: text, err: err}
	}()
	select {
	case <-cmd.Context().Done():
		return "", fmt.Errorf("%w: %w", errResetNotConfirmed, cmd.Context().Err())
	case l := <-read:
		if l.err != nil && !errors.Is(l.err, io.EOF) {
			return "", fmt.Errorf("read the answer: %w", l.err)
		}
		return l.text, nil
	}
}

// resetVolumes returns the services named, in catalog order without
// duplicates, and their volume keys. Each must keep data.
func resetVolumes(args []string) (names, keys []string, err error) {
	for _, svc := range catalog.All() {
		if !slices.Contains(args, svc.Name) {
			continue
		}
		if svc.Volume == "" {
			return nil, nil, fmt.Errorf("%s keeps no data, so there is nothing to reset", svc.Name)
		}
		names = append(names, svc.Name)
		keys = append(keys, svc.Volume)
	}
	return names, keys, nil
}

// confirmReset asks on the terminal before data is deleted, and refuses
// without one.
func (g *globals) confirmReset(cmd *cobra.Command, names, volumes []string) error {
	if !g.stdinIsTerminal() {
		return errors.New("reset deletes data, and standard input is not a terminal to " +
			"ask on; pass --yes to reset without asking")
	}
	question := fmt.Sprintf("Delete the data of %s in stack %s (volumes %s)? [y/N] ",
		strings.Join(names, ", "), g.stack, strings.Join(volumes, ", "))
	if len(volumes) == 0 {
		question = fmt.Sprintf("Recreate %s in stack %s with empty volumes? [y/N] ",
			strings.Join(names, ", "), g.stack)
	}
	if _, err := io.WriteString(cmd.ErrOrStderr(), question); err != nil {
		return fmt.Errorf("ask: %w", err)
	}
	answer, err := readAnswer(cmd)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return errResetNotConfirmed
	}
}
