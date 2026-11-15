package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// upStack starts services on a fake runtime from a directory of their own.
func upStack(t *testing.T, tuleYAML string, services ...string) (*fakeRuntime, string) {
	t.Helper()
	dir := writeConfig(t, freePorts(t)+tuleYAML)
	rt := &fakeRuntime{socketGroup: "989"}
	if _, err := runWith(t, rt, dir, append([]string{"up"}, services...)...); err != nil {
		t.Fatal(err)
	}
	return rt, dir
}

func TestLogs(t *testing.T) {
	rt, _ := upStack(t, "", "object-store", "queues")
	out, err := runWith(t, rt, t.TempDir(), "logs", "queues", "-f", "--since", "10m")
	if err != nil {
		t.Fatal(err)
	}
	if out != "logs of object-store,queues\n" {
		t.Errorf("logs went to standard output as %q", out)
	}
	if !slices.Equal(rt.calls, []string{"logs queues -f --since=10m"}) {
		t.Errorf("calls = %v", rt.calls)
	}
	if _, err = runWith(t, rt, t.TempDir(), "logs", "mailbox"); err == nil ||
		!strings.Contains(err.Error(), "does not run mailbox") {
		t.Errorf("logs mailbox = %v", err)
	}
	if _, err = runWith(t, &fakeRuntime{}, t.TempDir(), "logs"); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Errorf("logs without a stack = %v", err)
	}
}

func TestRestart(t *testing.T) {
	rt, _ := upStack(t, "", "object-store", "queues")
	out, err := runWith(t, rt, t.TempDir(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rt.calls, []string{"restart"}) ||
		!strings.Contains(out, "restarted object-store, queues in stack tule") {
		t.Errorf("calls = %v, output %q", rt.calls, out)
	}
	if _, err = runWith(t, rt, t.TempDir(), "restart", "queues"); err != nil {
		t.Fatal(err)
	}
	if rt.calls[1] != "restart queues" {
		t.Errorf("calls = %v", rt.calls)
	}
	if len(rt.ups) != 1 {
		t.Errorf("restart recreated the stack: %d ups", len(rt.ups))
	}
}

// healthCase is two containers, object-store and queues, in a state, and
// what notHealthy says of them.
type healthCase struct {
	set     func(objectStore, queues *runtime.Container)
	name    string
	err     string
	waiting []string
}

var healthCases = []healthCase{
	{name: "healthy", set: func(_, _ *runtime.Container) {}},
	{
		name: "starting and restarting",
		set: func(o, q *runtime.Container) {
			o.State = stateRestarting
			q.Health = "starting"
		},
		waiting: []string{catalog.ObjectStore, catalog.Queues},
	},
	{
		name: "unhealthy",
		set:  func(_, q *runtime.Container) { q.Health = "unhealthy" },
		err:  "queues is unhealthy",
	},
	{
		name: "exited",
		set:  func(o, _ *runtime.Container) { o.State = "exited" },
		err:  "object-store is exited",
	},
}

func TestNotHealthy(t *testing.T) {
	rt, _ := upStack(t, "", catalog.ObjectStore, catalog.Queues)
	names := []string{catalog.ObjectStore, catalog.Queues}
	for _, tc := range healthCases {
		t.Run(tc.name, func(t *testing.T) {
			containers := slices.Clone(rt.containers) // object-store, queues
			tc.set(&containers[0], &containers[1])
			st, err := stack.FromContainers("tule", containers, nil)
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := notHealthy(st, names)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("notHealthy() = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(waiting, tc.waiting) {
				t.Errorf("notHealthy() = %v, %v; want %v", waiting, err, tc.waiting)
			}
		})
	}
}

func TestWaitHealthyTimesOut(t *testing.T) {
	rt, _ := upStack(t, "", "queues")
	rt.containers[0].Health = "starting"
	err := waitHealthy(t.Context(), rt, "tule", []string{catalog.Queues}, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not healthy after 50ms: queues") {
		t.Errorf("waitHealthy() = %v", err)
	}
}

func TestResetAsks(t *testing.T) {
	rt, dir := upStack(t, "", "object-store", "queues")
	_, stderr, err := runInput(t, rt, dir, strings.NewReader("n\n"), "reset", "queues")
	if !errors.Is(err, errResetNotConfirmed) {
		t.Errorf("reset answered no = %v", err)
	}
	if !strings.Contains(
		stderr,
		"Delete the data of queues in stack tule (volumes tule_queuesdata)? [y/N]",
	) {
		t.Errorf("question = %q", stderr)
	}
	if len(rt.calls) != 0 || len(rt.removedVolumes) != 0 {
		t.Errorf("reset not confirmed changed the stack: %v, %v", rt.calls, rt.removedVolumes)
	}

	out, _, err := runInput(t, rt, dir, strings.NewReader("yes\n"), "reset", "queues")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rt.calls, []string{"rm queues", "up queues"}) ||
		!slices.Equal(rt.removedVolumes, []string{"tule_queuesdata"}) {
		t.Errorf("calls = %v, removed volumes = %v", rt.calls, rt.removedVolumes)
	}
	if !strings.Contains(out, "reset queues in stack tule") {
		t.Errorf("output = %q", out)
	}
	if got := selectionOf(t, rt); got != "object-store,queues" {
		t.Errorf("selection after reset = %s", got)
	}
}

func TestResetRefusesWithoutATerminal(t *testing.T) {
	rt, dir := upStack(t, "", "queues", "console")
	_, err := runWith(t, rt, dir, "reset", "queues")
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Errorf("reset without a terminal = %v", err)
	}
	if len(rt.calls) != 0 {
		t.Errorf("calls = %v", rt.calls)
	}
	if _, err = runWith(t, rt, dir, "reset", "console", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "console keeps no data") {
		t.Errorf("reset console = %v", err)
	}
	if _, err = runWith(t, rt, dir, "reset", "mailbox", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "does not run mailbox") {
		t.Errorf("reset mailbox = %v", err)
	}
	if _, err = runWith(t, rt, dir, "reset", "queues", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rt.calls, []string{"rm queues", "up queues"}) {
		t.Errorf("calls = %v", rt.calls)
	}
}

func TestResetFunctionsRemovesItsContainers(t *testing.T) {
	rt, dir := upStack(t, "", "functions")
	rt.functions = 2
	out, err := runWith(t, rt, dir, "reset", "functions", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed 2 function containers") {
		t.Errorf("output = %q", out)
	}
	if rt.probes != 1 {
		t.Errorf("probed %d times; reset should keep the running functions' group", rt.probes)
	}
}

// envOf runs tule env from dir and returns the variables it sets.
func envOf(t *testing.T, rt runtime.Runtime, dir string) (vars map[string]string, stderr string) {
	t.Helper()
	out, stderr, err := runCapture(t, rt, dir, "env")
	if err != nil {
		t.Fatal(err)
	}
	vars = map[string]string{}
	for line := range strings.Lines(out) {
		kv, ok := strings.CutPrefix(strings.TrimSpace(line), "export ")
		if !ok {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = strings.Trim(v, "'")
	}
	return vars, stderr
}

func TestEnvReadsTheRunningStackBack(t *testing.T) {
	key := "rootKey: {id: AKIAREADBACK00000, secret: read/back+secret}\npublicHost: dev.example\n"
	rt, dir := upStack(t, key+"ports: {queues: 7050}\n", "object-store", "queues")

	// Elsewhere, another configuration does not change what runs.
	elsewhere := writeConfig(t, "services: [mailbox]\n")
	vars, stderr := envOf(t, rt, elsewhere)
	if !strings.Contains(stderr, "read back from running stack tule: object-store,queues") {
		t.Errorf("stderr = %q", stderr)
	}
	want := map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIAREADBACK00000",
		"AWS_SECRET_ACCESS_KEY": "read/back+secret",
		"AWS_ENDPOINT_URL_SQS":  "http://localhost:7050",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	if !strings.HasPrefix(vars["AWS_ENDPOINT_URL_S3"], "http://127.0.0.1:") {
		t.Errorf("AWS_ENDPOINT_URL_S3 = %q", vars["AWS_ENDPOINT_URL_S3"])
	}
	if _, ok := vars["AWS_ENDPOINT_URL_SES"]; ok {
		t.Error("env names mailbox, which the stack does not run")
	}

	// From the stack's own directory, publicHost is the configured one.
	vars, _ = envOf(t, rt, dir)
	if got := vars["AWS_ENDPOINT_URL_SQS"]; got != "http://dev.example:7050" {
		t.Errorf("AWS_ENDPOINT_URL_SQS from the stack's directory = %q", got)
	}
}

func TestEnvReadBackRefusesAControlCharacter(t *testing.T) {
	rt, _ := upStack(t, "", "queues")
	rt.containers[0].Env["TULE_Auth_SecretAccessKey"] = "bad\nexport X=1"
	_, err := runWith(t, rt, t.TempDir(), "env")
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Errorf("env with a newline in the key = %v", err)
	}
}

func TestEnvReadBackSkipsAStoppedService(t *testing.T) {
	rt, _ := upStack(t, "", "object-store", "queues")
	rt.containers[1].State = "exited"
	rt.containers[1].Ports = nil
	vars, stderr := envOf(t, rt, t.TempDir())
	if _, ok := vars["AWS_ENDPOINT_URL_SQS"]; ok {
		t.Error("env has an endpoint for a container that publishes none")
	}
	if !strings.Contains(stderr, "queues publishes no API port (its container is exited)") {
		t.Errorf("stderr = %q", stderr)
	}
}
