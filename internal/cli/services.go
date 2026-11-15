package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
)

func newServicesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "List the services tule can run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeServices(cmd)
		},
	}
}

func writeServices(cmd *cobra.Command) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tAWS\tIMAGE\tPORT\tNOTES"); err != nil {
		return fmt.Errorf("print the services: %w", err)
	}
	for _, svc := range catalog.All() {
		ports := make([]string, 0, len(svc.Ports))
		for _, p := range svc.Ports {
			ports = append(ports, strconv.Itoa(p.Default))
		}
		aws := svc.AWS
		if aws == "" {
			aws = "-"
		}
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\n",
			svc.Name,
			aws,
			svc.ImageRef(svc.Version),
			strings.Join(ports, ","),
			svc.Notes,
		); err != nil {
			return fmt.Errorf("print the services: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print the services: %w", err)
	}
	return nil
}
