package config

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"tule.dev/cli/internal/catalog"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustParse(t *testing.T, doc string) (*Config, []string) {
	t.Helper()
	cfg, warnings, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return cfg, warnings
}

func TestDefaults(t *testing.T) {
	cfg, warnings := mustParse(t, "")
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if cfg.Bind != "127.0.0.1" || cfg.PublicHost != "localhost" || cfg.PortOffset != 0 {
		t.Errorf("bind %q, publicHost %q, portOffset %d", cfg.Bind, cfg.PublicHost, cfg.PortOffset)
	}
	if cfg.RootKey.ID != catalog.ExampleAccessKeyID ||
		cfg.RootKey.Secret != catalog.ExampleSecretAccessKey {
		t.Errorf("rootKey = %+v, want the example key", cfg.RootKey)
	}
	sel, err := cfg.Selection(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Names(), catalog.DefaultSelection().Names()) {
		t.Errorf("Selection() = %v, want the default set", sel.Names())
	}
}

func TestEveryKey(t *testing.T) {
	cfg, _ := mustParse(t, `
services: [object-store, queues]
ports: {object-store: 19000}
portOffset: 100
versions: {queues: 0.6.0}
rootKey: {id: AKIAMYOWNKEY0000, secret: mine}
bind: 0.0.0.0
publicHost: dev.example
env: {object-store: {TULE_Server_Domain: dev.example}}
`)
	if cfg.Bind != "0.0.0.0" || cfg.PublicHost != "dev.example" ||
		cfg.RootKey.ID != "AKIAMYOWNKEY0000" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Env["object-store"]["TULE_Server_Domain"] != "dev.example" {
		t.Errorf("env = %v", cfg.Env)
	}
	sel, err := cfg.Selection(nil)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := cfg.HostPorts(sel)
	if err != nil {
		t.Fatal(err)
	}
	if got := ports.Of("object-store", catalog.PortAPI); got != 19000 {
		t.Errorf("object-store port = %d, want the override, 19000", got)
	}
	if got := ports.Of("queues", catalog.PortAPI); got != 9150 {
		t.Errorf("queues port = %d, want 9050+100", got)
	}
}

func TestSelectionPrecedence(t *testing.T) {
	cfg, _ := mustParse(t, "services: [object-store, queues]")
	sel, err := cfg.Selection(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sel.Names(), []string{"object-store", "queues"}; !slices.Equal(got, want) {
		t.Errorf("configured selection = %v, want %v", got, want)
	}
	sel, err = cfg.Selection([]string{"mailbox"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sel.Names(), []string{"mailbox"}; !slices.Equal(got, want) {
		t.Errorf("named services = %v, want %v over the configured ones", got, want)
	}
	if _, err := cfg.Selection([]string{"console"}); err == nil {
		t.Error("console alone named: error = nil")
	}
}

func TestFunctionsPublishesItsAPIPortAlone(t *testing.T) {
	cfg, _ := mustParse(t, "portOffset: 1000\nports: {functions: 7000}")
	sel, err := catalog.NewSelection([]string{"functions"})
	if err != nil {
		t.Fatal(err)
	}
	ports, err := cfg.HostPorts(sel)
	if err != nil {
		t.Fatal(err)
	}
	if got := ports.Of("functions", catalog.PortAPI); got != 7000 {
		t.Errorf("api = %d, want 7000", got)
	}
	if got := len(ports["functions"]); got != 1 {
		t.Errorf("functions has %d host ports, want its API port alone", got)
	}
}

func TestVersionOverride(t *testing.T) {
	svc, err := catalog.Lookup("object-store")
	if err != nil {
		t.Fatal(err)
	}
	queues, err := catalog.Lookup("queues")
	if err != nil {
		t.Fatal(err)
	}
	// queues is pinned at its tested version, so only object-store warns.
	cfg, warnings := mustParse(t, "versions: {object-store: 0.12.0, queues: "+queues.Version+"}")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "object-store 0.12.0") ||
		!strings.Contains(warnings[0], svc.Version) {
		t.Errorf("warnings = %v, want one naming object-store 0.12.0 and %s", warnings, svc.Version)
	}
	if got := cfg.Version(svc); got != "0.12.0" {
		t.Errorf("Version() = %q, want the override", got)
	}
}

// errorCases are documents Parse refuses, and a part of each error.
var errorCases = []struct {
	name string
	doc  string
	want string
}{
	{
		"unknown service",
		"services: [object-store, s3]",
		`unknown service "s3"; the services are identity, accounts`,
	},
	{"unknown ports key", "ports: {sqs: 1}", `ports: unknown service "sqs"`},
	{"unknown versions key", "versions: {sqs: 1.0.0}", `versions: unknown service "sqs"`},
	{"unknown env key", "env: {sqs: {A: b}}", `env: unknown service "sqs"`},
	{"unknown field", "service: [queues]", "field service not found"},
	{"empty services", "services: []", "services: is empty"},
	{"console alone", "services: [console]", "console shows the other services"},
	{"port out of range", "ports: {queues: 70000}", "70000 is not a port"},
	{"offset past the last port", "portOffset: 60000", "past 65535"},
	{"negative offset", "portOffset: -1", "negative"},
	{
		"empty version",
		`versions: {queues: ""}`,
		`versions: queues: "" is not a release version`,
	},
	{
		"interpolated version",
		`versions: {queues: "${HOME}"}`,
		`"${HOME}" is not a release version`,
	},
	{"version with a v", `versions: {queues: v0.6.0}`, "not a release version"},
	{"version with a tag suffix", `versions: {queues: "0.6.0 "}`, "not a release version"},
	{"latest", `versions: {queues: latest}`, "not a release version"},
	{
		"public bind with the example secret",
		"bind: 0.0.0.0\nrootKey: {id: AKIAMYOWNKEY0000, secret: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY}",
		"example",
	},
	{
		"public bind with the example id",
		"bind: 0.0.0.0\nrootKey: {id: AKIAIOSFODNN7EXAMPLE, secret: mine}",
		"example",
	},
	{
		"root key id in env",
		"env: {queues: {TULE_Auth_AccessKeyID: AKIAMYOWNKEY0000}}",
		"set it with rootKey",
	},
	{
		"root key secret in env",
		"env: {object-store: {TULE_Auth_SecretAccessKey: x}}",
		"set it with rootKey",
	},
	{
		"lowercase root key id in env",
		"env: {queues: {tule_auth_accesskeyid: AKIAMYOWNKEY0000}}",
		"set it with rootKey",
	},
	{
		"uppercase root key secret in env",
		"env: {queues: {TULE_AUTH_SECRETACCESSKEY: x}}",
		"set it with rootKey",
	},
	{
		"mixed-case root key secret in env",
		"env: {mailbox: {Tule_Auth_SecretAccessKey: x}}",
		"set it with rootKey",
	},
	{"half a root key", "rootKey: {id: AKIAMYOWNKEY0000}", "rootKey: needs both"},
	{"bind not an IP", "bind: localhost", `bind: "localhost" is not an IP address`},
	{"public bind with the example key", "bind: 0.0.0.0", "example key"},
	{"public IPv6 bind with the example key", `bind: "::"`, "example key"},
	{"public host with a scheme", "publicHost: http://x", "publicHost"},
	{"bad env name", `env: {queues: {"A=B": c}}`, "not a variable name"},
}

func TestErrors(t *testing.T) {
	for _, tt := range errorCases {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestLoopbackBindsAllowTheExampleKey(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "127.0.0.2", `"::1"`} {
		if _, _, err := Parse([]byte("bind: " + bind)); err != nil {
			t.Errorf("bind %s: error = %v", bind, err)
		}
	}
	if _, _, err := Parse(
		[]byte("bind: 0.0.0.0\nrootKey: {id: AKIAMYOWNKEY0000, secret: mine}"),
	); err != nil {
		t.Errorf("public bind with an own key: error = %v", err)
	}
}

func TestDuplicateHostPorts(t *testing.T) {
	cfg, _ := mustParse(t, "ports: {object-store: 9010}")
	sel, err := catalog.NewSelection([]string{"object-store", "secret-store"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.HostPorts(sel)
	if err == nil || !strings.Contains(err.Error(), "host port 9010") {
		t.Errorf("HostPorts() error = %v, want a conflict on 9010", err)
	}
	only, err := catalog.NewSelection([]string{"object-store"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.HostPorts(only); err != nil {
		t.Errorf("without secret-store: error = %v", err)
	}
}

func TestLoadWalksUp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	writeFile(t, path, "services: [queues]\n")
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(Options{Dir: deep})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if !slices.Equal(cfg.Services, []string{"queues"}) {
		t.Errorf("Services = %v", cfg.Services)
	}
}

func TestLoadConfigFlagWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "services: [queues]\n")
	other := filepath.Join(root, "other.yaml")
	writeFile(t, other, "services: [mailbox]\n")

	cfg, _, err := Load(Options{Dir: root, File: other})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != other || !slices.Equal(cfg.Services, []string{"mailbox"}) {
		t.Errorf("Path %q, Services %v: want the --config file", cfg.Path, cfg.Services)
	}

	if _, _, err := Load(
		Options{Dir: root, File: filepath.Join(root, "missing.yaml")},
	); err == nil {
		t.Error("missing --config file: error = nil")
	}
}

func TestLoadErrorNamesTheFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	writeFile(t, path, "services: [s3]\n")
	_, _, err := Load(Options{Dir: root})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Load() error = %v, want one naming %s", err, path)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "")
	cfg, _, err := Load(Options{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Services != nil || cfg.Bind != DefaultBind {
		t.Errorf("cfg = %+v, want the defaults", cfg)
	}
}

// hostileKeyParts hold characters a shell or compose reads as special.
var hostileKeyParts = []string{
	"it's", "\u2018q\u2019", "\u201aq\u201b", `back\slash`, "$(touch pwned)", "${HOME}",
	"`touch pwned`", "a\nb", "a;b", "a b", `"q"`, "a|b", "a&b",
}

func TestRootKeyAllowlist(t *testing.T) {
	for _, bad := range hostileKeyParts {
		for _, doc := range []string{
			"rootKey: {id: " + yamlQuote(bad) + ", secret: mine}",
			"rootKey: {id: AKIAMYOWNKEY0000, secret: " + yamlQuote(bad) + "}",
		} {
			_, _, err := Parse([]byte(doc))
			if err == nil ||
				!strings.Contains(err.Error(), "rootKey: id and secret may hold only") {
				t.Errorf("%s: error = %v, want the allowlist error", doc, err)
			}
		}
	}
	if _, _, err := Parse([]byte("rootKey: {id: AKIA.MY_OWN-KEY00, secret: a+b/c=d}")); err != nil {
		t.Errorf("allowed characters: error = %v", err)
	}
}

func TestPublicHostAllowlist(t *testing.T) {
	bad := append([]string{
		"http://x", "x:1", "user@x", "-x", "x-", "x..y", "a_b",
		strings.Repeat("a", 64) + ".example",
	}, hostileKeyParts...)
	for _, host := range bad {
		_, _, err := Parse([]byte("publicHost: " + yamlQuote(host)))
		if err == nil || !strings.Contains(err.Error(), "publicHost:") {
			t.Errorf("publicHost %q: error = %v, want a publicHost error", host, err)
		}
	}
	for _, host := range []string{"localhost", "dev.example", "dev.example.", "host-1", "10.0.0.5", "::1", "fe80::1"} {
		if _, _, err := Parse([]byte("publicHost: " + yamlQuote(host))); err != nil {
			t.Errorf("publicHost %q: error = %v", host, err)
		}
	}
}

// yamlQuote double-quotes s for a YAML flow scalar.
func yamlQuote(s string) string {
	return strconv.Quote(s)
}

func TestReleaseVersions(t *testing.T) {
	for _, v := range []string{"0.6.0", "10.20.30", "0.14.0-rc.1", "1.0.0-alpha-2"} {
		if _, _, err := Parse([]byte("versions: {queues: " + strconv.Quote(v) + "}")); err != nil {
			t.Errorf("version %q: error = %v", v, err)
		}
	}
}

func TestValidateRunsTheBindGuard(t *testing.T) {
	cfg, _ := mustParse(t, "")
	cfg.Bind = "0.0.0.0"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "example") {
		t.Errorf("Validate() after setting a public bind = %v, want the example key error", err)
	}
}
