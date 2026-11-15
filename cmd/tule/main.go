// Command tule runs a local Tule stack.
package main

import (
	"os"

	"tule.dev/cli/internal/cli"
)

func main() {
	// Cobra prints the error itself; the exit code is what is left to set.
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
