//go:build e2e

// Package e2e runs the tule binary against a real Docker daemon: stacks
// started from the released images, and real AWS SDK clients against the
// real servers.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	tuleconfig "tule.dev/cli/internal/config"
	"tule.dev/cli/internal/stack"
)

// tuleBinary is the tule built for the run.
var tuleBinary string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "tule-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tuleBinary = filepath.Join(dir, "tule")
	//nolint:gosec // The e2e test builds its own binary.
	build := exec.Command("go", "build", "-o", tuleBinary, "tule.dev/cli/cmd/tule")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build tule:", err)
		return 1
	}
	return m.Run()
}

// stackDir is one stack: its name and the directory holding its tule.yaml.
type stackDir struct {
	t    *testing.T
	name string
	dir  string
}

// newStack makes a directory with tule.yaml for the stack name, and
// removes the stack and its volumes when the test ends.
func newStack(t *testing.T, name, tuleYAML string) *stackDir {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, tuleconfig.FileName),
		[]byte(tuleYAML),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	s := &stackDir{t: t, name: name, dir: dir}
	t.Cleanup(func() {
		// t.Context is already canceled when cleanups run.
		if out, err := s.run(context.Background(), s.dir, "down", "--volumes"); err != nil {
			t.Errorf("clean up stack %s: %v\n%s", name, err, out)
		}
	})
	return s
}

// configPath is the absolute path of the stack's tule.yaml.
func (s *stackDir) configPath() string {
	return filepath.Join(s.dir, tuleconfig.FileName)
}

// tule runs tule --stack <name> args in the stack's directory, and returns
// its combined output.
func (s *stackDir) tule(args ...string) (string, error) {
	return s.tuleIn(s.dir, args...)
}

// tuleIn runs tule --stack <name> args in dir.
func (s *stackDir) tuleIn(dir string, args ...string) (string, error) {
	return s.run(s.t.Context(), dir, args...)
}

func (s *stackDir) run(parent context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	//nolint:gosec // The e2e test runs the tule it built, with its own arguments.
	cmd := exec.CommandContext(ctx, tuleBinary, append([]string{"--stack", s.name}, args...)...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// must runs tule and fails the test when it fails.
func (s *stackDir) must(args ...string) string {
	s.t.Helper()
	out, err := s.tule(args...)
	if err != nil {
		s.t.Fatalf("tule --stack %s %s: %v\n%s", s.name, strings.Join(args, " "), err, out)
	}
	s.t.Logf("tule --stack %s %s:\n%s", s.name, strings.Join(args, " "), out)
	return out
}

// status runs tule status --json.
func (s *stackDir) status() map[string]stack.ServiceStatus {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), time.Minute)
	defer cancel()
	//nolint:gosec // The e2e test runs the tule it built, with its own arguments.
	cmd := exec.CommandContext(ctx, tuleBinary, "--stack", s.name, "status", "--json")
	cmd.Dir = s.t.TempDir() // status reads the stack back from anywhere.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("tule status: %v\n%s%s", err, out, stderr.String())
	}
	var parsed struct {
		Stack    string                `json:"stack"`
		Configs  []string              `json:"configs"`
		Services []stack.ServiceStatus `json:"services"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		s.t.Fatalf("parse tule status: %v\n%s", err, out)
	}
	if !slices.Equal(parsed.Configs, []string{s.configPath()}) {
		s.t.Errorf("status configs = %q, want %q", parsed.Configs, s.configPath())
	}
	byName := map[string]stack.ServiceStatus{}
	for _, svc := range parsed.Services {
		byName[svc.Name] = svc
	}
	return byName
}

// awsConfig evaluates tule env in sh, as eval "$(tule env)" does, sets the
// variables it exports in the test's environment, and loads the SDK's
// default config from them.
func (s *stackDir) awsConfig() aws.Config {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `eval "$("$TULE" env)" && env -0`)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "TULE="+tuleBinary, "AWS_SESSION_TOKEN=left-over")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("eval tule env: %v\n%s", err, stderr.String())
	}
	isolateAWSEnv(s.t)
	set := 0
	for kv := range strings.SplitSeq(string(out), "\x00") {
		k, v, _ := strings.Cut(kv, "=")
		if k == "AWS_SESSION_TOKEN" {
			s.t.Errorf("tule env left AWS_SESSION_TOKEN set")
		}
		if strings.HasPrefix(k, "AWS_") {
			s.t.Setenv(k, v)
			set++
		}
	}
	if set == 0 {
		s.t.Fatal("tule env set no AWS_ variables")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		s.t.Fatalf("load the default config: %v", err)
	}
	return cfg
}

// isolateAWSEnv keeps the runner's own AWS profile, shared files and
// endpoint settings out of the default config chain.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "AWS_") || k == "AWS_CONFIG_FILE" ||
			k == "AWS_SHARED_CREDENTIALS_FILE" {
			continue
		}
		t.Setenv(k, "") // Restores the value after the test.
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// docker runs the docker CLI and returns its trimmed standard output.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	//nolint:gosec // The e2e test's own docker commands.
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// containerEnv returns a container's environment.
func containerEnv(t *testing.T, container string) map[string]string {
	t.Helper()
	var env []string
	out := docker(t, "container", "inspect", "--format", "{{json .Config.Env}}", container)
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse %s's environment: %v", container, err)
	}
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// stackResources lists the stack's containers and volumes.
func stackResources(t *testing.T, name string) (containers, volumes string) {
	t.Helper()
	filter := "label=com.docker.compose.project=" + name
	return docker(t, "ps", "--all", "--quiet", "--filter", filter),
		docker(t, "volume", "ls", "--quiet", "--filter", filter)
}
