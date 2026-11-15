// Command tule runs a local Tule stack.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"tule.dev/cli/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	// An interrupt or a termination cancels the commands' context, which
	// interrupts compose and lets it exit, so tule cleans up after it. A
	// second signal, once the first has been taken, ends tule at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	// Cobra prints the error itself; the exit code is what is left to set.
	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}
