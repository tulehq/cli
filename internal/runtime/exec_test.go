//go:build !windows

package runtime

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tule.dev/cli/internal/render"
)

// fakeDocker puts a docker command on PATH that runs script, a POSIX sh
// script, with $LOG naming a file it may write to, and returns that file.
func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	//nolint:gosec // The test's own executable.
	if err := os.WriteFile(filepath.Join(dir, "docker"),
		[]byte("#!/bin/sh\nLOG='"+log+"'\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // The test's own file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestInterruptLetsComposeExitAndCleansUp cancels a compose call, as Ctrl-C
// cancels the commands' context: compose must get an interrupt, not a kill,
// and the env file must be gone after it exits.
func TestInterruptLetsComposeExitAndCleansUp(t *testing.T) {
	log := fakeDocker(t, `
for a in "$@"; do
	case "$prev" in --env-file) echo "envfile $a" >> "$LOG" ;; esac
	prev="$a"
done
trap 'echo interrupted >> "$LOG"; exit 130' INT
echo started >> "$LOG"
while :; do sleep 0.05; done
`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- NewCompose(io.Discard).Up(ctx, &render.Project{Name: "tule"})
	}()
	waitForLine(t, log, "started")
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Up() = nil after an interrupt")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Up() did not return after an interrupt")
	}
	got := readLog(t, log)
	if !strings.Contains(got, "interrupted") {
		t.Errorf("compose was not interrupted:\n%s", got)
	}
	envFile := loggedEnvFile(got)
	if envFile == "" {
		t.Fatalf("compose was given no --env-file:\n%s", got)
	}
	if _, err := os.Stat(filepath.Dir(envFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the env file's directory is left after the interrupt: %v", err)
	}
}

// waitForLine waits until the log holds line.
func waitForLine(t *testing.T, log, line string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(log) //nolint:gosec // The test's own file.
		if strings.Contains(string(data), line) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake compose did not log %q", line)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// loggedEnvFile is the --env-file the fake compose logged.
func loggedEnvFile(log string) string {
	for line := range strings.Lines(log) {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "envfile "); ok {
			return path
		}
	}
	return ""
}

// TestInterruptReachesThePlugin stands docker in for a CLI that does not
// pass the interrupt on, as docker does not when its output is a terminal:
// the plugin it runs, a process of its own, must get it from tule.
func TestInterruptReachesThePlugin(t *testing.T) {
	log := fakeDocker(t, `
export LOG
sh -c 'trap "echo plugin-interrupted >> \"\$LOG\"; exit 130" INT
echo started >> "$LOG"
while :; do sleep 0.05; done'
`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- NewCompose(io.Discard).Up(ctx, &render.Project{Name: "tule"})
	}()
	waitForLine(t, log, "started")
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Up() = nil after an interrupt")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Up() did not return after an interrupt")
	}
	waitForLine(t, log, "plugin-interrupted")
}

func TestSocketGroup(t *testing.T) {
	log := fakeDocker(t, `echo "$*" >> "$LOG"; echo 989`)
	gid, err := NewCompose(io.Discard).SocketGroup(
		t.Context(), "ghcr.io/tulehq/functions:0.7.0", "/var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	if gid != "989" {
		t.Errorf("SocketGroup() = %q, want 989", gid)
	}
	want := "run --rm --network none --entrypoint stat --volume /var/run/docker.sock:/s " +
		"ghcr.io/tulehq/functions:0.7.0 -c %g /s\n"
	if got := readLog(t, log); got != want {
		t.Errorf("docker ran with\n%q\nwant\n%q", got, want)
	}
}

func TestSocketGroupRefusesWhatIsNotAGroupID(t *testing.T) {
	fakeDocker(t, `echo "stat: can't stat '/s'"`)
	_, err := NewCompose(io.Discard).SocketGroup(t.Context(), "img", "/var/run/docker.sock")
	if err == nil || !strings.Contains(err.Error(), "not a group ID") {
		t.Errorf("SocketGroup() = %v, want a group ID error", err)
	}
}

func TestServiceVerbs(t *testing.T) {
	log := fakeDocker(t, `shift 7; echo "$*" >> "$LOG"`)
	c := NewCompose(io.Discard)
	p := &render.Project{Name: "tule"}
	if err := c.UpServices(t.Context(), p, []string{"queues"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveServices(t.Context(), p, []string{"queues", "functions"}); err != nil {
		t.Fatal(err)
	}
	want := "up --detach --wait --no-deps queues\nrm --stop --force queues functions\n"
	if got := readLog(t, log); got != want {
		t.Errorf("compose ran\n%s\nwant\n%s", got, want)
	}
}

func TestLogsGoToTheirWriter(t *testing.T) {
	fakeDocker(t, `echo out; echo err >&2`)
	var progress, logs strings.Builder
	err := NewCompose(&progress).Logs(t.Context(), &render.Project{Name: "tule"},
		LogsOptions{Out: &logs})
	if err != nil {
		t.Fatal(err)
	}
	if logs.String() != "out\n" || progress.String() != "err\n" {
		t.Errorf("logs = %q, progress = %q", logs.String(), progress.String())
	}
}
