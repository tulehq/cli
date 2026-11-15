package cli

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
)

// newVersionCommand returns `tule version`, which prints what readBuildInfo
// reports, then the image and version of each service in the catalog. `go install` sets no ldflags, so the build information is the only
// record of the version: the module version for a release, "(devel)" or a
// pseudo-version for a build from a checkout, which also carries the VCS
// revision.
func newVersionCommand(readBuildInfo func() (*debug.BuildInfo, bool)) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tule version and the service versions it runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, ok := readBuildInfo()
			if !ok {
				return errors.New("build information is not available in this binary")
			}
			return writeVersion(cmd.OutOrStdout(), info, catalog.All())
		},
	}
}

// writeVersion prints the version line, a blank line and a table of the
// services with their image references.
func writeVersion(out io.Writer, info *debug.BuildInfo, services []*catalog.Service) error {
	if _, err := fmt.Fprintf(out, "%s\n\n", versionString(info)); err != nil {
		return fmt.Errorf("print the version: %w", err)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SERVICE\tIMAGE"); err != nil {
		return fmt.Errorf("print the version: %w", err)
	}
	for _, svc := range services {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", svc.Name, svc.ImageRef(svc.Version)); err != nil {
			return fmt.Errorf("print the version: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("print the version: %w", err)
	}
	return nil
}

// versionString formats the module version, followed by the VCS revision and
// whether the tree was modified when the build recorded them.
func versionString(info *debug.BuildInfo) string {
	version := info.Main.Version
	if version == "" {
		version = "(devel)"
	}

	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	switch {
	case revision == "":
		return "tule " + version
	case modified:
		return fmt.Sprintf("tule %s (%s, modified)", version, revision)
	default:
		return fmt.Sprintf("tule %s (%s)", version, revision)
	}
}
