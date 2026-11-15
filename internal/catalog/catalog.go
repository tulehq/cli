// Package catalog holds the Tule services the CLI runs: their images, the
// versions tested together, their ports and data, and how each is wired to
// the others in a selection.
//
// The data mirrors console's compose.yaml, the stack every service is tested
// in; the parity test in internal/render holds the two together.
package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Registry is where the service images are published.
const Registry = "ghcr.io/tulehq"

// DataMount is where every service with data keeps it in its container.
const DataMount = "/data"

// Region is the region every service defaults to, and the one clients sign
// for.
const Region = "us-east-1"

// The AWS documentation's example key, the root key console's compose.yaml
// configures. identity holds access key IDs to at least 16 characters.
//
//nolint:gosec // A published example key, not a credential.
const (
	ExampleAccessKeyID     = "AKIAIOSFODNN7EXAMPLE"
	ExampleSecretAccessKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

// Port names. Every service has an API port; functions also has the port its
// function containers reach the Runtime API at.
const (
	PortAPI     = "api"
	PortRuntime = "runtime"
)

// Port is one port a service listens on.
type Port struct {
	// Name is PortAPI or PortRuntime.
	Name string
	// Default is the container port, and the host port before any offset.
	Default int
}

// Publish is one published port: the host port and the container port it
// forwards to.
type Publish struct {
	Host      int
	Container int
}

// RootKey is the one key every service accepts, and identity's root key.
type RootKey struct {
	ID     string
	Secret string
}

// HostPorts maps a service name to its host ports by port name.
type HostPorts map[string]map[string]int

// Of returns the host port of a service's port, or 0 when it has none.
func (h HostPorts) Of(service, port string) int {
	return h[service][port]
}

// Wiring is what a service's environment is derived from.
type Wiring struct {
	HostPorts HostPorts
	RootKey   RootKey
	// PublicHost is the host clients on this machine reach the services at.
	PublicHost string
	Selection  Selection
}

// Service is one catalog entry.
type Service struct {
	wire    func(w Wiring) map[string]string
	publish func(host map[string]int) []Publish

	// Name is the service's name, its compose service name and its host
	// name on the stack network.
	Name string
	// AWS is the AWS service it stands in for; empty for the console.
	AWS string
	// Image is the image repository, without a tag.
	Image string
	// Version is the tested version, the image tag.
	Version string
	// Volume is the named volume mounted at DataMount; empty when the
	// service keeps no data.
	Volume string
	// Notes is a line for `tule services`.
	Notes string
	// Ports lists the ports it listens on; the first is the API port.
	Ports []Port
	// EndpointVars are the AWS_ENDPOINT_URL_<SERVICE> suffixes SDK clients
	// of it read.
	EndpointVars []string
	// DependsOn lists the services it waits to be healthy for, when they
	// are selected.
	DependsOn []string
	// OptIn keeps it out of the default set.
	OptIn bool
	// DockerSocket marks a service that runs containers through the Docker
	// daemon, and so mounts its socket.
	DockerSocket bool
}

// ImageRef returns the image reference at version.
func (s *Service) ImageRef(version string) string {
	return s.Image + ":" + version
}

// URL is the service's base URL on the stack network.
func (s *Service) URL() string {
	return serviceURL(s.Name)
}

// Env returns the service's environment for a wiring: the root key, unless
// it takes none, and an endpoint for each selected service it talks to.
func (s *Service) Env(w Wiring) map[string]string {
	return s.wire(w)
}

// Publishes returns the ports to publish given the service's host ports.
func (s *Service) Publishes(host map[string]int) []Publish {
	if s.publish != nil {
		return s.publish(host)
	}
	out := make([]Publish, 0, len(s.Ports))
	for _, p := range s.Ports {
		out = append(out, Publish{Host: host[p.Name], Container: p.Default})
	}
	return out
}

// Service names.
const (
	Identity    = "identity"
	Accounts    = "accounts"
	ObjectStore = "object-store"
	SecretStore = "secret-store"
	Mailbox     = "mailbox"
	Queues      = "queues"
	Functions   = "functions"
	Console     = "console"
)

// All returns every service, in catalog order.
func All() []*Service {
	return services
}

// Names returns every service name, in catalog order.
func Names() []string {
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Name)
	}
	return names
}

// Lookup returns the service named name.
func Lookup(name string) (*Service, error) {
	for _, s := range services {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, UnknownError{Name: name}
}

// UnknownError reports a name that is not in the catalog.
type UnknownError struct {
	Name string
}

func (e UnknownError) Error() string {
	return fmt.Sprintf("unknown service %q; the services are %s",
		e.Name, strings.Join(Names(), ", "))
}

// Selection is a set of catalog services, kept in catalog order.
type Selection struct {
	names []string
}

// ErrConsoleAlone is returned for a selection of the console and nothing
// else: it is a console for the other services, and has none to show.
var ErrConsoleAlone = errors.New(
	"console shows the other services and needs at least one of them selected with it",
)

// NewSelection returns the selection of names, which must be known, and not
// the console alone. Duplicates are dropped.
func NewSelection(names []string) (Selection, error) {
	if len(names) == 0 {
		return Selection{}, errors.New("no services selected")
	}
	picked := make(map[string]bool, len(names))
	for _, n := range names {
		if _, err := Lookup(n); err != nil {
			return Selection{}, err
		}
		picked[n] = true
	}
	if len(picked) == 1 && picked[Console] {
		return Selection{}, ErrConsoleAlone
	}
	var sel Selection
	for _, s := range services {
		if picked[s.Name] {
			sel.names = append(sel.names, s.Name)
		}
	}
	return sel, nil
}

// DefaultSelection is every service that is not opt-in.
func DefaultSelection() Selection {
	var sel Selection
	for _, s := range services {
		if !s.OptIn {
			sel.names = append(sel.names, s.Name)
		}
	}
	return sel
}

// Has reports whether name is selected.
func (s Selection) Has(name string) bool {
	return slices.Contains(s.names, name)
}

// Names returns the selected names, in catalog order.
func (s Selection) Names() []string {
	return slices.Clone(s.names)
}

// Services returns the selected services, in catalog order.
func (s Selection) Services() []*Service {
	out := make([]*Service, 0, len(s.names))
	for _, svc := range services {
		if s.Has(svc.Name) {
			out = append(out, svc)
		}
	}
	return out
}

// String joins the names with commas.
func (s Selection) String() string {
	return strings.Join(s.names, ",")
}
