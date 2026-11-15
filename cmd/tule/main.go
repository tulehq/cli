// Command tule runs a local Tule stack.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tule.dev/cli/internal/cli"
	"tule.dev/cli/internal/runtime"
)

func main() {
	os.Exit(run())
}

// exitKilled is the exit status after a second signal.
const exitKilled = 1

func run() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go handleSignals(sigs, cancel, func() {
		if err := runtime.KillAll(); err != nil {
			fmt.Fprintln(os.Stderr, "tule:", err)
		}
		os.Exit(exitKilled)
	})
	// Cobra prints the error itself; the exit code is what is left to set.
	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}

// handleSignals cancels the commands' context on the first interrupt,
// termination or hangup, which interrupts compose and lets it exit, so tule
// cleans up after it. Compose runs in a process group of its own, out of
// the terminal's reach, so a second signal does not end tule alone: it
// kills compose, then tule.
func handleSignals(sigs <-chan os.Signal, cancel, kill func()) {
	<-sigs
	cancel()
	<-sigs
	kill()
}
