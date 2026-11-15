package stack

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
)

// Plan is what running a project changes in a stack.
type Plan struct {
	// Recreate lists the running services whose wiring or ports change.
	Recreate []Change
	// Versions lists the services whose version changes.
	Versions []VersionChange
	// Add lists the services that have no container yet.
	Add []string
	// Remove lists the services whose containers go.
	Remove []string
}

// Change is a running service that is recreated, and why.
type Change struct {
	Service string
	// Reasons say what changed, such as "TULE_Storage_QueuesEndpoint
	// removed". Values are left out: they may hold the root key.
	Reasons []string
}

// VersionChange is a running service whose version changes.
type VersionChange struct {
	Service string
	From    string
	To      string
}

// NewPlan compares the project p, published on bindings, with the stack s.
func NewPlan(s *State, p *render.Project, bindings []Binding) Plan {
	var plan Plan
	for _, svc := range catalog.All() {
		want, selected := p.Services[svc.Name]
		have, running := s.Containers[svc.Name]
		switch {
		case !selected && running:
			plan.Remove = append(plan.Remove, svc.Name)
		case selected && !running:
			plan.Add = append(plan.Add, svc.Name)
		case selected && running:
			plan.compare(svc.Name, have, want, bindings)
		}
	}
	return plan
}

// compare adds what changes in a running service.
func (p *Plan) compare(
	name string,
	have runtime.Container,
	want render.Service,
	bindings []Binding,
) {
	if from, to := imageTag(have.Image), imageTag(unescape(want.Image)); from != to {
		p.Versions = append(p.Versions, VersionChange{Service: name, From: from, To: to})
	}
	reasons := envChanges(have, want.Environment)
	if portsChanged(have, bindings, name) {
		reasons = append(reasons, "host ports changed")
	}
	if len(reasons) > 0 {
		p.Recreate = append(p.Recreate, Change{Service: name, Reasons: reasons})
	}
}

// envChanges names the variables that differ between a container and the
// rendered environment, which is escaped for compose. A variable the
// container has that its image does not set is tule's, and dropped when the
// render no longer has it.
func envChanges(have runtime.Container, rendered map[string]string) []string {
	want := make(map[string]string, len(rendered))
	for k, v := range rendered {
		want[k] = unescape(v)
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(want)) {
		v, ok := have.Env[k]
		switch {
		case !ok:
			out = append(out, k+" added")
		case v != want[k]:
			out = append(out, k+" changed")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(have.Env)) {
		if _, ok := want[k]; ok {
			continue
		}
		if image, ok := have.ImageEnv[k]; ok && image == have.Env[k] {
			continue
		}
		out = append(out, k+" removed")
	}
	return out
}

// portsChanged reports whether a container's published ports differ from
// the service's bindings.
func portsChanged(have runtime.Container, bindings []Binding, service string) bool {
	var want []runtime.Port
	for _, b := range bindings {
		if b.Service == service {
			want = append(
				want,
				runtime.Port{HostIP: b.HostIP, HostPort: b.HostPort, Container: b.Container},
			)
		}
	}
	if len(want) != len(have.Ports) {
		return true
	}
	for _, w := range want {
		if !slices.ContainsFunc(have.Ports, func(p runtime.Port) bool {
			return p.HostPort == w.HostPort && p.Container == w.Container &&
				sameIP(p.HostIP, w.HostIP)
		}) {
			return true
		}
	}
	return false
}

// imageTag is the tag of an image reference, the service version.
func imageTag(ref string) string {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") {
		return "latest"
	}
	return ref[i+1:]
}

// unescape undoes the render's escaping of $ for compose.
func unescape(s string) string {
	return strings.ReplaceAll(s, "$$", "$")
}

// Write prints the plan: version changes, then the services recreated, then
// those removed and added. It prints nothing when nothing changes.
func (p Plan) Write(w io.Writer) error {
	var b strings.Builder
	if len(p.Versions) > 0 {
		b.WriteString("Version changes:\n")
		for _, v := range p.Versions {
			fmt.Fprintf(&b, "  %s %s -> %s\n", v.Service, v.From, v.To)
		}
	}
	if len(p.Recreate) > 0 {
		b.WriteString("Recreating, because their wiring changed:\n")
		for _, c := range p.Recreate {
			fmt.Fprintf(&b, "  %s: %s\n", c.Service, strings.Join(c.Reasons, ", "))
		}
	}
	if len(p.Remove) > 0 {
		fmt.Fprintf(&b, "Removing: %s\n", strings.Join(p.Remove, ", "))
	}
	if len(p.Add) > 0 {
		fmt.Fprintf(&b, "Starting: %s\n", strings.Join(p.Add, ", "))
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("print the plan: %w", err)
	}
	return nil
}
