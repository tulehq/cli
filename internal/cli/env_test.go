package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
)

// run executes the root command with args from dir, and returns its output.
func run(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, config.FileName),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnvVariables(t *testing.T) {
	cfg, _, err := config.Parse([]byte("publicHost: dev.example\nportOffset: 1"))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := catalog.NewSelection(catalog.Names())
	if err != nil {
		t.Fatal(err)
	}
	vars, err := envVariables(cfg, sel)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	order := make([]string, 0, len(vars))
	for _, v := range vars {
		got[v.name] = v.value
		order = append(order, v.name)
	}
	want := map[string]string{ //nolint:gosec // The example key.
		"AWS_ACCESS_KEY_ID":                catalog.ExampleAccessKeyID,
		"AWS_SECRET_ACCESS_KEY":            catalog.ExampleSecretAccessKey,
		"AWS_REGION":                       "us-east-1",
		"AWS_DEFAULT_REGION":               "us-east-1",
		"AWS_ENDPOINT_URL_IAM":             "http://dev.example:9031",
		"AWS_ENDPOINT_URL_STS":             "http://dev.example:9031",
		"AWS_ENDPOINT_URL_ORGANIZATIONS":   "http://dev.example:9021",
		"AWS_ENDPOINT_URL_S3":              "http://dev.example:9001",
		"AWS_ENDPOINT_URL_SECRETS_MANAGER": "http://dev.example:9011",
		"AWS_ENDPOINT_URL_SES":             "http://dev.example:9041",
		"AWS_ENDPOINT_URL_SESV2":           "http://dev.example:9041",
		"AWS_ENDPOINT_URL_SQS":             "http://dev.example:9051",
		"AWS_ENDPOINT_URL_LAMBDA":          "http://dev.example:9061",
	}
	if len(got) != len(want) {
		t.Errorf("variables = %v, want %v", order, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestEnvOnlySelectedServices(t *testing.T) {
	dir := writeConfig(t, "services: [queues]\n")
	out, _, err := run(t, dir, "env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AWS_ENDPOINT_URL_SQS='http://localhost:9050'") {
		t.Errorf("output lacks the SQS endpoint:\n%s", out)
	}
	if strings.Contains(out, "AWS_ENDPOINT_URL_S3") {
		t.Errorf("output has an S3 endpoint without object-store:\n%s", out)
	}
}

func TestWriteEnvShells(t *testing.T) {
	vars := []variable{{"AWS_ACCESS_KEY_ID", "AKIA"}, {"AWS_SECRET_ACCESS_KEY", `it's\x`}}
	tests := []struct {
		shell string
		want  string
	}{
		{"sh", "export AWS_ACCESS_KEY_ID='AKIA'\n" +
			"export AWS_SECRET_ACCESS_KEY='it'\\''s\\x'\n" +
			"unset AWS_SESSION_TOKEN\n"},
		{"fish", "set -gx AWS_ACCESS_KEY_ID 'AKIA'\n" +
			"set -gx AWS_SECRET_ACCESS_KEY 'it\\'s\\\\x'\n" +
			"set -e AWS_SESSION_TOKEN\n"},
		{"powershell", "$env:AWS_ACCESS_KEY_ID = 'AKIA'\n" +
			"$env:AWS_SECRET_ACCESS_KEY = 'it''s\\x'\n" +
			"Remove-Item Env:AWS_SESSION_TOKEN -ErrorAction SilentlyContinue\n"},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			var b bytes.Buffer
			if err := writeEnv(&b, tt.shell, vars); err != nil {
				t.Fatal(err)
			}
			if b.String() != tt.want {
				t.Errorf("output:\n%s\nwant:\n%s", b.String(), tt.want)
			}
		})
	}
	if err := writeEnv(&bytes.Buffer{}, "zsh", vars); err == nil {
		t.Error("unknown shell: error = nil")
	}
}

func TestComposeCommand(t *testing.T) {
	dir := writeConfig(t, "services: [object-store]\nversions: {object-store: 0.12.0}\n")
	out := filepath.Join(dir, "compose.yaml")
	stdout, stderr, err := run(t, dir, "--stack", "dev", "compose", "-o", out)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing with -o", stdout)
	}
	if !strings.Contains(stderr, "warning: versions: object-store 0.12.0") {
		t.Errorf("stderr = %q, want the version warning", stderr)
	}
	data, err := os.ReadFile(out) //nolint:gosec // The file this test wrote.
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: dev\n",
		"image: ghcr.io/tulehq/object-store:0.12.0",
		"dev.tule.config: " + filepath.Join(dir, config.FileName),
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("compose file lacks %q:\n%s", want, data)
		}
	}

	stdout, _, err = run(t, dir, "compose", "queues", "--docker-gid", "989")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "image: ghcr.io/tulehq/queues:") ||
		strings.Contains(stdout, "object-store") {
		t.Errorf("named services should replace the configured ones:\n%s", stdout)
	}
}

func TestComposeCommandErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := run(
		t,
		dir,
		"compose",
		"console",
	); err == nil ||
		!strings.Contains(err.Error(), "console") {
		t.Errorf("console alone: error = %v", err)
	}
	if _, _, err := run(t, dir, "--stack", "Bad", "compose"); err == nil {
		t.Error("bad stack: error = nil")
	}
	if _, _, err := run(
		t,
		dir,
		"--config",
		filepath.Join(dir, "none.yaml"),
		"compose",
	); err == nil {
		t.Error("missing --config: error = nil")
	}
}

func TestServicesCommand(t *testing.T) {
	out, _, err := run(t, t.TempDir(), "services")
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range catalog.All() {
		if !strings.Contains(out, svc.ImageRef(svc.Version)) {
			t.Errorf("output lacks %s:\n%s", svc.Name, out)
		}
	}
}

func TestS3EndpointHost(t *testing.T) {
	tests := []struct {
		publicHost string
		want       string
	}{
		{"localhost", "http://127.0.0.1:9000"},
		{"10.0.0.5", "http://10.0.0.5:9000"},
		{"::1", "http://[::1]:9000"},
		{"dev.example", "http://dev.example:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.publicHost, func(t *testing.T) {
			cfg, _, err := config.Parse([]byte("publicHost: " + strconv.Quote(tt.publicHost)))
			if err != nil {
				t.Fatal(err)
			}
			sel, err := catalog.NewSelection([]string{catalog.ObjectStore})
			if err != nil {
				t.Fatal(err)
			}
			vars, err := envVariables(cfg, sel)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range vars {
				if v.name == "AWS_ENDPOINT_URL_S3" {
					if v.value != tt.want {
						t.Errorf("AWS_ENDPOINT_URL_S3 = %q, want %q", v.value, tt.want)
					}
					return
				}
			}
			t.Error("no AWS_ENDPOINT_URL_S3")
		})
	}
}

func TestComposeFunctionsNeedsTheGroup(t *testing.T) {
	dir := t.TempDir()
	_, _, err := run(t, dir, "compose", "functions")
	if err == nil {
		t.Fatal("functions without --docker-gid: error = nil")
	}
	for _, want := range []string{
		"--docker-gid", "stat -c %g /var/run/docker.sock", "Docker Desktop",
		"docker run --rm --entrypoint stat -v /var/run/docker.sock:/s ghcr.io/tulehq/functions:" +
			functionsVersion + " -c %g /s",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if _, _, err = run(t, dir, "compose", "queues"); err != nil {
		t.Errorf("queues without --docker-gid: error = %v", err)
	}
	out, _, err := run(t, dir, "compose", "functions", "--docker-gid", "989")
	if err != nil || !strings.Contains(out, `- "989"`) {
		t.Errorf("functions with --docker-gid 989: error = %v, output:\n%s", err, out)
	}
}

func TestComposeValidatesTheGroupWithoutFunctions(t *testing.T) {
	dir := t.TempDir()
	for _, gid := range []string{"", "docker", "${DOCKER_GID}", "-1"} {
		_, _, err := run(t, dir, "compose", "queues", "--docker-gid", gid)
		if err == nil || !strings.Contains(err.Error(), "--docker-gid") {
			t.Errorf(
				"--docker-gid %q without functions: error = %v, want a --docker-gid error",
				gid,
				err,
			)
		}
	}
}
