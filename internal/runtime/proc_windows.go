//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// startInGroup leaves cmd in tule's console. On Windows, Ctrl-C in the
// console reaches compose directly; a stop sent to tule alone does not,
// and compose is killed once the grace has passed.
func startInGroup(*exec.Cmd) {}

// interruptGroup asks docker to stop. Windows does not deliver os.Interrupt
// to another process, so this fails, and docker is killed after the grace.
func interruptGroup(cmd *exec.Cmd) error {
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("interrupt docker compose: %w", err)
	}
	return nil
}

// killGroup kills docker; on Windows the plugin is not reached.
func killGroup(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill docker compose: %w", err)
	}
	return nil
}

// endGroup has no group to wait for on Windows.
func endGroup(*exec.Cmd, time.Time) error { return nil }
