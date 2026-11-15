package cli

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// newVersionCommand returns `tule version`, which prints what readBuildInfo
// reports. `go install` sets no ldflags, so the build information is the only
// record of the version: the module version for a release, "(devel)" or a
// pseudo-version for a build from a checkout, which also carries the VCS
// revision.
func newVersionCommand(readBuildInfo func() (*debug.BuildInfo, bool)) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tule version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, ok := readBuildInfo()
			if !ok {
				return errors.New("build information is not available in this binary")
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), versionString(info)); err != nil {
				return fmt.Errorf("print the version: %w", err)
			}
			return nil
		},
	}
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
