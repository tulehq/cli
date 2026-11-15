package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// healthWait bounds how long restart waits for the services it restarted
// to be healthy again, and healthPoll is how often it looks.
const (
	healthWait = 3 * time.Minute
	healthPoll = 500 * time.Millisecond
)

func newRestartCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "restart [service...]",
		Short: "Restart the stack's services, keeping their data",
		Long: "Restart the containers of the services named, or of every service of the\n" +
			"stack, and wait until they are healthy again. Containers are restarted, not\n" +
			"recreated, so their configuration and their data stay as they are. The stack is\n" +
			"read back from its containers, so it works from any directory.",
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
			if err = rt.Restart(cmd.Context(), stateProject(st), args); err != nil {
				return err //nolint:wrapcheck // The runtime says what failed.
			}
			names := args
			if len(names) == 0 {
				names = st.Services()
			}
			if err = waitHealthy(cmd.Context(), rt, g.stack, names, healthWait); err != nil {
				return err
			}
			return writeLine(cmd, fmt.Sprintf("restarted %s in stack %s",
				strings.Join(names, ", "), g.stack))
		},
	}
}

// waitHealthy waits until every service named is running and, when its
// image has a health check, healthy. It fails as soon as one has stopped or
// is unhealthy, and after timeout.
func waitHealthy(
	ctx context.Context,
	rt runtime.Runtime,
	name string,
	services []string,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		st, err := stack.Read(ctx, rt, name)
		if err != nil {
			return err //nolint:wrapcheck // Says which stack and what failed.
		}
		waiting, err := notHealthy(st, services)
		if err != nil || len(waiting) == 0 {
			return err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("not healthy after %s: %s", timeout, strings.Join(waiting, ", "))
			}
			return fmt.Errorf("waiting for %s to be healthy: %w",
				strings.Join(waiting, ", "), ctx.Err())
		case <-time.After(healthPoll):
		}
	}
}

// stateRestarting is Docker's state of a container between its stop and its
// start.
const stateRestarting = "restarting"

// notHealthy returns the services that are starting, and fails for one that
// is gone, stopped or unhealthy.
func notHealthy(st *stack.State, services []string) ([]string, error) {
	var waiting []string
	for _, svc := range services {
		c, ok := st.Containers[svc]
		switch {
		case !ok:
			return nil, fmt.Errorf("%s's container is gone", svc)
		case c.State == stateRestarting:
			waiting = append(waiting, svc)
		case c.State != stack.StateRunning:
			return nil, fmt.Errorf(
				"%s is %s after the restart; see tule logs %s",
				svc,
				c.State,
				svc,
			)
		case c.Health == "unhealthy":
			return nil, fmt.Errorf("%s is unhealthy after the restart; see tule logs %s", svc, svc)
		case c.Health != "" && c.Health != stack.HealthHealthy:
			waiting = append(waiting, svc)
		}
	}
	return waiting, nil
}
