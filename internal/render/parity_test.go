package render

import (
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"tule.dev/cli/internal/catalog"
)

// consoleComposeEnv names the path of console's compose.yaml for the parity
// check. CI fetches the file at a pinned console ref and sets it.
const consoleComposeEnv = "TULE_CONSOLE_COMPOSE"

// parityService is the part of a compose service the parity check compares.
// Every other key fails the check unless keyRules lists it as ignored.
type parityService struct {
	Environment map[string]string     `yaml:"environment"`
	DependsOn   map[string]Dependency `yaml:"depends_on"`
	Image       string                `yaml:"image"`
	Ports       []string              `yaml:"ports"`
	Volumes     []string              `yaml:"volumes"`
	Profiles    []string              `yaml:"profiles"`
	GroupAdd    []string              `yaml:"group_add"`
}

type parityProject struct {
	Services map[string]*parityService `yaml:"services"`
	Volumes  map[string]*parityVolume  `yaml:"volumes"`
}

// parityVolume is a top-level volume. Neither side labels its volumes; the
// labels are read so a label on either side shows as a difference.
type parityVolume struct {
	Labels map[string]string `yaml:"labels"`
}

// keyRules says which keys a side of the parity check may hold: the keys
// compared, and the keys ignored, each with its reason. Any other key, at any
// level, fails the check, so a setting the parity types do not read (restart,
// user, command, extra_hosts, networks...) cannot go unnoticed.
type keyRules struct {
	// ignoredTop and ignoredService map an ignored key to its reason.
	ignoredTop     map[string]string
	ignoredService map[string]string
	// extensions allows top-level x- keys.
	extensions bool
}

// The keys compared, at each level.
var (
	comparedTopKeys     = []string{"services", "volumes"}
	comparedServiceKeys = []string{
		"image",
		"environment",
		"ports",
		"volumes",
		"depends_on",
		"profiles",
		"group_add",
	}
	comparedDependencyKeys = []string{"condition"}
	comparedVolumeKeys     = []string{"labels"}
)

var (
	// composeKeys are the keys console's compose.yaml may hold.
	composeKeys = keyRules{
		// x- keys are extension fields: console's holds the anchors of the
		// root key and identity's endpoint, which are merged into the
		// services and compared there.
		extensions: true,
	}
	// renderKeys are the keys the render may hold.
	renderKeys = keyRules{
		ignoredTop: map[string]string{
			"name": "the project name, the stack; compose's file has none and takes its directory's name",
		},
		ignoredService: map[string]string{
			"labels": "the dev.tule.* labels are the CLI's state; compose's services have none",
		},
	}
)

// unknownKeys returns every key of doc that rules neither compares nor
// ignores, as a dotted path.
func unknownKeys(t *testing.T, doc []byte, rules keyRules) []string {
	t.Helper()
	var top map[string]any
	if err := yaml.Unmarshal(doc, &top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range top {
		if rules.extensions && strings.HasPrefix(k, "x-") {
			delete(top, k)
		}
	}
	out := appendUnknown(nil, "", top, comparedTopKeys, rules.ignoredTop)
	for name, svc := range asMap(top["services"]) {
		m := asMap(svc)
		out = appendUnknown(out, "services."+name+".", m, comparedServiceKeys, rules.ignoredService)
		for dep, cond := range asMap(m["depends_on"]) {
			out = appendUnknown(
				out,
				"services."+name+".depends_on."+dep+".",
				asMap(cond),
				comparedDependencyKeys,
				nil,
			)
		}
	}
	for name, v := range asMap(top["volumes"]) {
		out = appendUnknown(out, "volumes."+name+".", asMap(v), comparedVolumeKeys, nil)
	}
	slices.Sort(out)
	return out
}

// appendUnknown appends the path of each key of m that is neither compared
// nor ignored.
func appendUnknown(
	out []string,
	path string,
	m map[string]any,
	compared []string,
	ignored map[string]string,
) []string {
	for k := range m {
		if slices.Contains(compared, k) {
			continue
		}
		if _, ok := ignored[k]; ok {
			continue
		}
		out = append(out, path+k)
	}
	return out
}

// asMap is v as a mapping; anything else, which the typed decode reports,
// is empty here.
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// paritySide is one project's services and volumes, after decoding.
type paritySide struct {
	services map[string]*parityService
	// volumeLabels maps each top-level volume to its labels.
	volumeLabels map[string]map[string]string
}

// difference is one intended difference between console's compose.yaml and
// the full-selection render with the default configuration. apply rewrites
// the compose side to what the render has instead, and reports whether it
// changed anything: an entry that no longer applies fails the test, so the
// table cannot go stale.
type difference struct {
	apply  func(compose *paritySide) bool
	name   string
	reason string
}

// paritySelection is every service: compose keeps functions behind a
// profile, but its file configures it, and its wiring is what is compared.
func paritySelection() []string { return catalog.Names() }

// parityDocker is what the render is given for the socket: the socket compose
// mounts, and group 0, what compose's ${DOCKER_GID:-0} is when unset.
var parityDocker = Docker{SocketPath: "/var/run/docker.sock", GroupID: "0"}

var intendedDifferences = []difference{
	{
		name: "publish bind",
		reason: "compose publishes on every interface; the CLI publishes on bind, " +
			"127.0.0.1 by default, so a stack holding the example key is not reachable " +
			"from other machines",
		apply: func(c *paritySide) bool {
			changed := false
			for _, s := range c.services {
				for i, p := range s.Ports {
					s.Ports[i] = "127.0.0.1:" + p
					changed = true
				}
			}
			return changed
		},
	},
	{
		name: "functions profile",
		reason: "compose keeps functions behind a profile; the CLI keeps it out of the " +
			"default set instead, and renders it only when it is selected",
		apply: func(c *paritySide) bool {
			s := c.services[catalog.Functions]
			if s == nil || !slices.Equal(s.Profiles, []string{"functions"}) {
				return false
			}
			s.Profiles = nil
			return true
		},
	},
	{
		name: "DOCKER_GID interpolation",
		reason: "compose reads the socket's group from DOCKER_GID at up, defaulting to " +
			"0; the CLI writes the group it is given (the --docker-gid flag for now, " +
			"probed from the daemon later); the check gives it 0",
		apply: func(c *paritySide) bool {
			s := c.services[catalog.Functions]
			if s == nil || !slices.Equal(s.GroupAdd, []string{"${DOCKER_GID:-0}"}) {
				return false
			}
			s.GroupAdd = []string{parityDocker.GroupID}
			return true
		},
	},
}

func decodeParity(t *testing.T, data []byte) *paritySide {
	t.Helper()
	var p parityProject
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	side := &paritySide{services: p.Services, volumeLabels: map[string]map[string]string{}}
	for name, v := range p.Volumes {
		if v == nil {
			side.volumeLabels[name] = nil
			continue
		}
		side.volumeLabels[name] = v.Labels
	}
	return side
}

// TestParityWithConsoleCompose compares the catalog with console's
// compose.yaml: per service, the image and tag, the environment after wiring
// for every service, the ports, the mounts, depends_on, profiles and
// group_add, and the volumes. Only the differences in intendedDifferences
// are allowed.
func TestParityWithConsoleCompose(t *testing.T) {
	path := os.Getenv(consoleComposeEnv)
	if path == "" {
		t.Skipf(
			"%s is unset; set it to the path of console's compose.yaml to check the catalog against it",
			consoleComposeEnv,
		)
	}
	data, err := os.ReadFile(path) //nolint:gosec // The file the variable names.
	if err != nil {
		t.Fatalf("%s: %v", consoleComposeEnv, err)
	}
	if unknown := unknownKeys(t, data, composeKeys); len(unknown) > 0 {
		t.Errorf(
			"%s holds keys the parity check does not compare: %v; compare them, or ignore them with a reason",
			path,
			unknown,
		)
	}
	compose := decodeParity(t, data)
	for _, d := range intendedDifferences {
		if !d.apply(compose) {
			t.Errorf(
				"intended difference %q no longer applies to %s; remove it from the table",
				d.name,
				path,
			)
		}
	}

	renderedYAML := renderYAML(t, Input{
		Stack:     "tule",
		Config:    parse(t, ""),
		Selection: selection(t, paritySelection()...),
		Docker:    parityDocker,
	})
	if unknown := unknownKeys(t, renderedYAML, renderKeys); len(unknown) > 0 {
		t.Errorf("the render holds keys the parity check does not compare: %v", unknown)
	}
	rendered := decodeParity(t, renderedYAML)

	names := slices.Sorted(maps.Keys(compose.services))
	if got := slices.Sorted(maps.Keys(rendered.services)); !slices.Equal(got, names) {
		t.Fatalf("services: render has %v, compose has %v", got, names)
	}
	for _, name := range names {
		compareService(t, name, compose.services[name], rendered.services[name])
	}
	if !reflect.DeepEqual(compose.volumeLabels, rendered.volumeLabels) {
		t.Errorf("volumes:\n compose %v\n render  %v", compose.volumeLabels, rendered.volumeLabels)
	}
}

func compareService(t *testing.T, name string, compose, rendered *parityService) {
	t.Helper()
	if compose.Image != rendered.Image {
		t.Errorf("%s image: compose %s, render %s", name, compose.Image, rendered.Image)
	}
	for _, k := range slices.Sorted(maps.Keys(compose.Environment)) {
		if got, ok := rendered.Environment[k]; !ok || got != compose.Environment[k] {
			t.Errorf("%s %s: compose %q, render %q", name, k, compose.Environment[k], got)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(rendered.Environment)) {
		if _, ok := compose.Environment[k]; !ok {
			t.Errorf(
				"%s %s: render sets %q, compose does not set it",
				name,
				k,
				rendered.Environment[k],
			)
		}
	}
	same := func(field string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s %s: compose %v, render %v", name, field, a, b)
		}
	}
	same("ports", compose.Ports, rendered.Ports)
	same("volumes", compose.Volumes, rendered.Volumes)
	same("depends_on", compose.DependsOn, rendered.DependsOn)
	same("profiles", compose.Profiles, rendered.Profiles)
	same("group_add", compose.GroupAdd, rendered.GroupAdd)
}

// TestParityReadsMergeKeys holds the decoder to compose's YAML merge keys,
// which console's compose.yaml uses for the root key and identity's
// endpoint.
func TestParityReadsMergeKeys(t *testing.T) {
	side := decodeParity(t, []byte(strings.Join([]string{
		"x-a: &a {A: a}",
		"x-b: &b {B: b}",
		"services:",
		"  s:",
		"    image: i",
		"    environment:",
		"      <<: [*a, *b]",
		"      C: c",
		"volumes:",
		"  v:",
	}, "\n")))
	want := map[string]string{"A": "a", "B": "b", "C": "c"}
	if got := side.services["s"].Environment; !maps.Equal(got, want) {
		t.Errorf("environment = %v, want %v", got, want)
	}
	if labels, ok := side.volumeLabels["v"]; !ok || labels != nil {
		t.Errorf("volume v = %v, %v; want present with no labels", labels, ok)
	}
}

func TestParityRejectsUnknownKeys(t *testing.T) {
	doc := []byte(strings.Join([]string{
		"x-key: &key {A: a}",
		"networks: {n: {}}",
		"services:",
		"  s:",
		"    image: i",
		"    environment: {<<: *key}",
		"    restart: always",
		"    user: root",
		"    command: [run]",
		"    extra_hosts: [h:1.2.3.4]",
		"    labels: {l: v}",
		"    depends_on: {d: {condition: service_healthy, restart: true}}",
		"volumes:",
		"  v: {driver: local}",
	}, "\n"))
	want := []string{
		"networks",
		"services.s.command",
		"services.s.depends_on.d.restart",
		"services.s.extra_hosts",
		"services.s.labels",
		"services.s.restart",
		"services.s.user",
		"volumes.v.driver",
	}
	if got := unknownKeys(t, doc, composeKeys); !slices.Equal(got, want) {
		t.Errorf("unknownKeys(compose) = %v\nwant %v", got, want)
	}
	// The render may hold labels and a name; extension keys are console's.
	render := []byte("name: tule\nx-a: 1\nservices: {s: {image: i, labels: {l: v}}}\n")
	if got, want := unknownKeys(t, render, renderKeys), []string{"x-a"}; !slices.Equal(got, want) {
		t.Errorf("unknownKeys(render) = %v, want %v", got, want)
	}
}
