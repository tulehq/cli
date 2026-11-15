package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/stack"
)

// statusJSON is `tule status --json`.
type statusJSON struct {
	Stack string `json:"stack"`
	// Configs are the tule.yaml files the stack's containers were started
	// from, an empty path for none: one, unless they disagree.
	Configs  []string              `json:"configs"`
	Services []stack.ServiceStatus `json:"services"`
}

func newStatusCommand(g *globals) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the stack's services: state, health, version, address and volume",
		Long: "Show each service of the stack, read back from its containers, so it works from\n" +
			"any directory. Exits non-zero when the stack has no containers, or a service is\n" +
			"not running and healthy.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := g.runtime(cmd)
			if err != nil {
				return err
			}
			st, err := g.readState(cmd, rt)
			if err != nil {
				return err
			}
			if err = warnConfigConflict(cmd, st); err != nil {
				return err
			}
			statuses := st.Status()
			if asJSON {
				err = writeStatusJSON(
					cmd,
					statusJSON{Stack: g.stack, Configs: st.Configs(), Services: statuses},
				)
			} else if st.Running() {
				err = writeStatusTable(cmd, statuses)
			}
			if err != nil {
				return err
			}
			return statusError(g.stack, statuses)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// statusError fails a stack without containers, and one with a service that
// is not running and healthy.
func statusError(name string, statuses []stack.ServiceStatus) error {
	if len(statuses) == 0 {
		return fmt.Errorf("stack %q is not running; start it with tule up", name)
	}
	var unhealthy []string
	for _, s := range statuses {
		if !s.Healthy {
			unhealthy = append(unhealthy, s.Name)
		}
	}
	if len(unhealthy) > 0 {
		return fmt.Errorf("not healthy: %s", strings.Join(unhealthy, ", "))
	}
	return nil
}

func writeStatusJSON(cmd *cobra.Command, s statusJSON) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("print the status: %w", err)
	}
	return nil
}

func writeStatusTable(cmd *cobra.Command, statuses []stack.ServiceStatus) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SERVICE\tSTATE\tHEALTH\tVERSION\tADDRESS\tVOLUME"); err != nil {
		return fmt.Errorf("print the status: %w", err)
	}
	for _, s := range statuses {
		state := s.State
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, state, dash(s.Health), dash(s.Version), dash(s.Address), dash(s.Volume),
		); err != nil {
			return fmt.Errorf("print the status: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print the status: %w", err)
	}
	return nil
}

// dash stands in for an empty column.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
