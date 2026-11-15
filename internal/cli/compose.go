package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/render"
)

// functionsService is the catalog's functions.
var functionsService = mustLookup(catalog.Functions)

// functionsVersion is the tested functions version, for the hint below.
var functionsVersion = functionsService.Version

func mustLookup(name string) *catalog.Service {
	svc, err := catalog.Lookup(name)
	if err != nil {
		panic(err)
	}
	return svc
}

// errNoDockerGID is returned when functions is selected without the socket's
// group. The platform notes are functions' README's.
var errNoDockerGID = errors.New(
	`functions needs the group that owns the Docker socket: pass --docker-gid.
  Linux: --docker-gid "$(stat -c %g /var/run/docker.sock)"
  Docker Desktop (macOS, Windows): the group the socket has inside Docker Desktop's VM, not
  the one stat reports on the host, which this prints:
    docker run --rm --entrypoint stat -v /var/run/docker.sock:/s ghcr.io/tulehq/functions:` + functionsVersion + ` -c %g /s
  functions' README marks the Docker Desktop setup untried, and says to run the image as
  root when the socket there is owned by root with no group access, which tule does not do`,
)

// dockerSocket is the Docker daemon's socket on Linux and in Docker Desktop's
// VM.
const dockerSocket = "/var/run/docker.sock"

func newComposeCommand(g *globals) *cobra.Command {
	var out, dockerGID string
	cmd := &cobra.Command{
		Use:   "compose [service...]",
		Short: "Write the compose file for a selection of services",
		Long: "Write the compose file for the services named, or, with none named, the\n" +
			"configured services or the default set: every service but functions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.loadConfig(cmd)
			if err != nil {
				return err
			}
			sel, err := cfg.Selection(args)
			if err != nil {
				return fmt.Errorf("compose: %w", err)
			}
			if err = checkDockerGID(cmd, sel, dockerGID); err != nil {
				return err
			}
			project, err := render.Render(render.Input{
				Stack:     g.stack,
				Config:    cfg,
				Selection: sel,
				Docker:    render.Docker{SocketPath: dockerSocket, GroupID: dockerGID},
			})
			if err != nil {
				return fmt.Errorf("compose: %w", err)
			}
			data, err := project.YAML()
			if err != nil {
				return fmt.Errorf("compose: %w", err)
			}
			if out == "" {
				if _, err := cmd.OutOrStdout().Write(data); err != nil {
					return fmt.Errorf("write the compose file: %w", err)
				}
				return nil
			}
			// The file holds the root key.
			if err := os.WriteFile(out, data, 0o600); err != nil {
				return fmt.Errorf("write the compose file: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write to this file instead of standard output")
	cmd.Flags().StringVar(&dockerGID, "docker-gid", "",
		"the group ID that owns the Docker socket on the daemon's host, given to functions; required with functions")
	return cmd
}

// checkDockerGID validates --docker-gid whenever it is given, and requires it
// when functions is selected.
func checkDockerGID(cmd *cobra.Command, sel catalog.Selection, gid string) error {
	if err := checkGIDFlag(cmd, gid); err != nil {
		return err
	}
	if sel.Has(catalog.Functions) && gid == "" {
		return errNoDockerGID
	}
	return nil
}
