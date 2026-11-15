package render

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// testDocker stands in for the probed socket group.
var testDocker = Docker{SocketPath: "/var/run/docker.sock", GroupID: "989"}

func parse(t *testing.T, doc string) *config.Config {
	t.Helper()
	cfg, _, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	return cfg
}

func renderYAML(t *testing.T, in Input) []byte {
	t.Helper()
	p, err := Render(in)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	data, err := p.YAML()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func selection(t *testing.T, names ...string) catalog.Selection {
	t.Helper()
	sel, err := catalog.NewSelection(names)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

type goldenCase struct {
	in   func(t *testing.T) Input
	name string
}

var goldenCases = []goldenCase{
	{
		name: "default",
		in: func(t *testing.T) Input {
			return Input{Stack: "tule", Config: parse(t, ""), Selection: catalog.DefaultSelection()}
		},
	},
	{
		name: "full",
		in: func(t *testing.T) Input {
			cfg := parse(t, "")
			cfg.Path = "/work/tule.yaml"
			return Input{
				Stack:     "tule",
				Config:    cfg,
				Selection: selection(t, catalog.Names()...),
				Docker:    testDocker,
			}
		},
	},
	{
		name: "minimal",
		in: func(t *testing.T) Input {
			return Input{
				Stack:     "tule",
				Config:    parse(t, ""),
				Selection: selection(t, catalog.ObjectStore),
			}
		},
	},
	{
		name: "functions",
		in: func(t *testing.T) Input {
			cfg := parse(t, `
portOffset: 100
ports: {queues: 19050}
bind: 0.0.0.0
publicHost: dev.example
rootKey: {id: AKIAMYOWNKEY0000, secret: own+secret/key=}
env: {functions: {TULE_Functions_PullImages: "true", TULE_Example: "pa$$word"}}
`)
			return Input{
				Stack: "second", Config: cfg,
				Selection: selection(t, catalog.Functions, catalog.Queues, catalog.Identity),
				Docker:    testDocker,
			}
		},
	},
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderYAML(t, tc.in(t))
			path := filepath.Join("testdata", tc.name+".yaml")
			if *update {
				if err := os.WriteFile(path, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path) //nolint:gosec // A golden file of this test.
			if err != nil {
				t.Fatalf("%v (run go test ./internal/render -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("render differs from %s (run with -update after checking):\n%s", path, got)
			}
			if again := renderYAML(t, tc.in(t)); !bytes.Equal(got, again) {
				t.Error("two renders of the same input differ")
			}
		})
	}
}

// TestComposeAcceptsTheRender asks compose itself to validate each golden
// input's render. No container is started.
func TestComposeAcceptsTheRender(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not on PATH; skipping the compose validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // docker from PATH.
	if out, err := exec.CommandContext(ctx, docker, "compose", "version").
		CombinedOutput(); err != nil {
		t.Skipf("docker compose is not available (%v): %s", err, out)
	}
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yaml")
			if err := os.WriteFile(path, renderYAML(t, tc.in(t)), 0o600); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // docker from PATH, on a file this test wrote.
			cmd := exec.CommandContext(ctx, docker, "compose", "-f", path, "config", "-q")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("docker compose config -q: %v\n%s", err, out)
			}
		})
	}
}

func TestDollarsAreEscaped(t *testing.T) {
	cfg := parse(t, `env: {queues: {TULE_Example: "a$b"}}`)
	cfg.Path = "/home/$USER/tule.yaml"
	p, err := Render(Input{Stack: "tule", Config: cfg, Selection: selection(t, catalog.Queues)})
	if err != nil {
		t.Fatal(err)
	}
	q := p.Services[catalog.Queues]
	if got := q.Environment["TULE_Example"]; got != "a$$b" {
		t.Errorf("env value = %q, want a$$b", got)
	}
	if got := q.Labels[LabelConfig]; got != "/home/$$USER/tule.yaml" {
		t.Errorf("config label = %q", got)
	}
}

func TestEnvOverridesWiring(t *testing.T) {
	cfg := parse(
		t,
		`env: {queues: {TULE_Queues_BaseURL: "http://queues:9050", TULE_GasEnv: development}}`,
	)
	p, err := Render(Input{Stack: "tule", Config: cfg, Selection: selection(t, catalog.Queues)})
	if err != nil {
		t.Fatal(err)
	}
	env := p.Services[catalog.Queues].Environment
	if env["TULE_Queues_BaseURL"] != "http://queues:9050" || env["TULE_GasEnv"] != "development" {
		t.Errorf("environment = %v", env)
	}
}

func TestExistingVolumesKeepTheirVersion(t *testing.T) {
	cfg := parse(t, "")
	p, err := Render(Input{
		Stack:          "tule",
		Config:         cfg,
		Selection:      selection(t, catalog.Queues, catalog.ObjectStore),
		VolumeVersions: map[string]string{"queuesdata": "0.5.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Volumes["queuesdata"].Labels[LabelVersion]; got != "0.5.0" {
		t.Errorf("queuesdata version = %q, want the one it was created with", got)
	}
	objectStore, err := catalog.Lookup(catalog.ObjectStore)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Volumes["objectdata"].Labels[LabelVersion]; got != objectStore.Version {
		t.Errorf("objectdata version = %q, want %q", got, objectStore.Version)
	}
}

func TestIPv6Bind(t *testing.T) {
	cfg := parse(t, `bind: "::1"`)
	p, err := Render(
		Input{Stack: "tule", Config: cfg, Selection: selection(t, catalog.ObjectStore)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services[catalog.ObjectStore].Ports; len(got) != 1 || got[0] != "[::1]:9000:9000" {
		t.Errorf("ports = %v, want [[::1]:9000:9000]", got)
	}
}

func TestErrors(t *testing.T) {
	cfg := parse(t, "")
	_, err := Render(Input{Stack: "tule", Config: cfg, Selection: selection(t, catalog.Functions)})
	if err == nil || !strings.Contains(err.Error(), "Docker socket") {
		t.Errorf("functions without Docker: error = %v", err)
	}
	_, err = Render(Input{Stack: "My Stack", Config: cfg, Selection: selection(t, catalog.Queues)})
	if err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("bad stack: error = %v", err)
	}
	dup := parse(t, "ports: {object-store: 9050}")
	_, err = Render(
		Input{
			Stack:     "tule",
			Config:    dup,
			Selection: selection(t, catalog.ObjectStore, catalog.Queues),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "9050") {
		t.Errorf("duplicate port: error = %v", err)
	}
}

func TestGroupIDMustBeDecimal(t *testing.T) {
	for _, gid := range []string{"${DOCKER_GID}", "docker", "-1", "1 2", "0x10"} {
		_, err := Render(Input{
			Stack: "tule", Config: parse(t, ""), Selection: selection(t, catalog.Functions),
			Docker: Docker{SocketPath: "/var/run/docker.sock", GroupID: gid},
		})
		if err == nil || !strings.Contains(err.Error(), "not a group ID") {
			t.Errorf("gid %q: error = %v, want a group ID error", gid, err)
		}
	}
}

func TestRenderRunsTheBindGuard(t *testing.T) {
	cfg := parse(t, "")
	cfg.Bind = "0.0.0.0"
	_, err := Render(Input{Stack: "tule", Config: cfg, Selection: selection(t, catalog.Queues)})
	if err == nil || !strings.Contains(err.Error(), "example") {
		t.Errorf("Render() = %v, want the example key error", err)
	}
}

// TestEveryStringIsEscaped puts a $ in every string a service can carry and
// holds the output to doubling each one.
func TestEveryStringIsEscaped(t *testing.T) {
	s := escapeService(Service{
		Image:       "i:$V",
		Environment: map[string]string{"K": "$V"},
		Ports:       []string{"$V:1:1"},
		Volumes:     []string{"$V:/data"},
		GroupAdd:    []string{"$V"},
		Labels:      map[string]string{"L": "$V"},
	})
	want := Service{
		Image:       "i:$$V",
		Environment: map[string]string{"K": "$$V"},
		Ports:       []string{"$$V:1:1"},
		Volumes:     []string{"$$V:/data"},
		GroupAdd:    []string{"$$V"},
		Labels:      map[string]string{"L": "$$V"},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("escapeService() = %+v\nwant %+v", s, want)
	}
	// Every string field of Service is covered above: a new one fails here
	// until it is escaped and added.
	if n := reflect.TypeFor[Service]().NumField(); n != 7 {
		t.Errorf("Service has %d fields; escape the new one in escapeService and add it here", n)
	}
}
