//go:build !windows

package runtime

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// startInGroup starts cmd in a process group of its own, which the compose
// plugin docker runs joins. When its output is a terminal, docker does not
// pass a signal on to the plugin: it leaves that to the terminal, which
// signals the whole foreground group on Ctrl-C. A signal sent to tule
// alone, by kill or a supervisor, would reach neither, so tule signals the
// group itself.
func startInGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interruptGroup sends SIGINT to cmd's process group, then SIGCONT, so a
// group stopped for writing to the terminal (stty tostop) wakes to handle
// it.
func interruptGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		return fmt.Errorf("interrupt docker compose: %w", err)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT); err != nil {
		return fmt.Errorf("continue docker compose: %w", err)
	}
	return nil
}

// killGroup kills cmd's process group at once.
func killGroup(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill docker compose: %w", err)
	}
	return nil
}

// groupPoll is how often endGroup looks for the group's processes.
const groupPoll = 100 * time.Millisecond

// endGroup waits until deadline for every process of cmd's group to exit,
// as the plugin may outlive docker, then kills those left.
func endGroup(cmd *exec.Cmd, deadline time.Time) error {
	pgid := -cmd.Process.Pid
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(groupPoll)
	}
	return killGroup(cmd)
}
