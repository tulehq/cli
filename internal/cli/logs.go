package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

func newLogsCommand(g *globals) *cobra.Command {
	var opts runtime.LogsOptions
	cmd := &cobra.Command{
		Use:   "logs [service...]",
		Short: "Show the logs of the stack's services",
		Long: "Show the logs of the services named, or of every service of the stack, from\n" +
			"compose. The stack is read back from its containers, so it works from any\n" +
			"directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := g.runtime(cmd)
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
			opts.Services = args
			opts.Out = cmd.OutOrStdout()
			if err := rt.Logs(cmd.Context(), stateProject(st), opts); err != nil {
				return err //nolint:wrapcheck // The runtime says what failed.
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&opts.Follow, "follow", "f", false, "keep printing new output")
	cmd.Flags().StringVar(&opts.Since, "since", "",
		"show logs since a timestamp, such as 2026-11-15T10:00:00Z, or a duration ago, such as 10m")
	return cmd
}

// checkRunning checks the stack has containers, and one for each service
// named.
func checkRunning(st *stack.State, names []string) error {
	if !st.Running() {
		return fmt.Errorf("stack %q is not running; start it with tule up", st.Name)
	}
	services := st.Services()
	for _, name := range names {
		if _, err := catalog.Lookup(name); err != nil {
			return err //nolint:wrapcheck // Names the services.
		}
		if !slices.Contains(services, name) {
			return fmt.Errorf("stack %q does not run %s; it runs %s",
				st.Name, name, strings.Join(services, ", "))
		}
	}
	return nil
}
