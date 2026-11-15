// Package render turns a selection and a configuration into a compose
// project: the file `tule compose` writes and `tule up` runs.
package render

import (
	"fmt"
	"maps"
	"net"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
)

// Labels set on every service. Each is the same for a service whatever
// else is selected, so a change of selection does not enter the config
// hash compose recreates containers by. Volumes carry no tule labels:
// compose asks to recreate a volume, deleting its data, when the labels it
// was created with change, and console's compose.yaml creates them with
// none.
const (
	LabelStack   = "dev.tule.stack"
	LabelService = "dev.tule.service"
	LabelConfig  = "dev.tule.config"
)

// Project is a compose project. Its fields are in the order they are
// written; maps are written in key order, so the output is deterministic.
//
//nolint:govet // The field order is the order of the compose file.
type Project struct {
	Name     string             `yaml:"name"`
	Services map[string]Service `yaml:"services"`
	Volumes  map[string]Volume  `yaml:"volumes,omitempty"`
}

// Service is a compose service.
//
//nolint:govet // The field order is the order of the compose file.
type Service struct {
	Image       string                `yaml:"image"`
	DependsOn   map[string]Dependency `yaml:"depends_on,omitempty"`
	Environment map[string]string     `yaml:"environment,omitempty"`
	Ports       []string              `yaml:"ports,omitempty"`
	Volumes     []string              `yaml:"volumes,omitempty"`
	GroupAdd    []string              `yaml:"group_add,omitempty"`
	Labels      map[string]string     `yaml:"labels"`
}

// Dependency is a depends_on entry.
type Dependency struct {
	Condition string `yaml:"condition"`
}

// Volume is a named volume, with compose's defaults.
type Volume struct{}

// Docker is how a service that runs containers reaches the Docker daemon:
// the daemon's socket, and the group that owns it on the daemon's host,
// which the service is given as a supplementary group.
type Docker struct {
	SocketPath string
	GroupID    string
}

// Input is what a project is rendered from.
type Input struct {
	Config *config.Config
	// Docker is required when a selected service mounts the Docker socket.
	Docker Docker
	// Stack is the project name.
	Stack     string
	Selection catalog.Selection
}

// stackName is what compose accepts as a project name.
var stackName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidateStack checks a stack name is a compose project name.
func ValidateStack(name string) error {
	if !stackName.MatchString(name) {
		return fmt.Errorf(
			"stack %q: use lowercase letters, digits, dashes and underscores, starting with a letter or digit",
			name,
		)
	}
	return nil
}

// NetworkName is the network compose makes for a stack, its default
// network, named as Docker names it.
func NetworkName(stack string) string {
	return stack + "_default"
}

// Render returns the compose project for in.
func Render(in Input) (*Project, error) {
	if err := ValidateStack(in.Stack); err != nil {
		return nil, err
	}
	if err := in.Config.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	hostPorts, err := in.Config.HostPorts(in.Selection)
	if err != nil {
		return nil, fmt.Errorf("host ports: %w", err)
	}
	wiring := catalog.Wiring{
		Selection:  in.Selection,
		HostPorts:  hostPorts,
		PublicHost: in.Config.PublicHost,
		RootKey:    in.Config.RootKey,
		Network:    NetworkName(in.Stack),
	}
	p := &Project{Name: in.Stack, Services: map[string]Service{}, Volumes: map[string]Volume{}}
	for _, svc := range in.Selection.Services() {
		s, err := renderService(in, svc, wiring)
		if err != nil {
			return nil, err
		}
		p.Services[svc.Name] = s
		if svc.Volume != "" {
			p.Volumes[svc.Volume] = Volume{}
		}
	}
	return p, nil
}

func renderService(in Input, svc *catalog.Service, wiring catalog.Wiring) (Service, error) {
	version := in.Config.Version(svc)
	env := svc.Env(wiring)
	maps.Copy(env, in.Config.Env[svc.Name])

	s := Service{
		Image:       svc.ImageRef(version),
		Environment: env,
		Labels: map[string]string{
			LabelStack:   in.Stack,
			LabelService: svc.Name,
			LabelConfig:  in.Config.Path,
		},
	}
	for _, dep := range svc.DependsOn {
		if !in.Selection.Has(dep) {
			continue
		}
		if s.DependsOn == nil {
			s.DependsOn = map[string]Dependency{}
		}
		s.DependsOn[dep] = Dependency{Condition: "service_healthy"}
	}
	for _, pub := range svc.Publishes(wiring.HostPorts[svc.Name]) {
		s.Ports = append(s.Ports, publish(in.Config.Bind, pub))
	}
	if svc.Volume != "" {
		s.Volumes = append(s.Volumes, svc.Volume+":"+catalog.DataMount)
	}
	if svc.DockerSocket {
		if in.Docker.SocketPath == "" || in.Docker.GroupID == "" {
			return Service{}, fmt.Errorf(
				"%s runs containers through Docker and needs the Docker socket and its group",
				svc.Name,
			)
		}
		if err := ValidateGroupID(in.Docker.GroupID); err != nil {
			return Service{}, fmt.Errorf("%s: %w", svc.Name, err)
		}
		s.Volumes = append(s.Volumes, in.Docker.SocketPath+":/var/run/docker.sock")
		s.GroupAdd = []string{in.Docker.GroupID}
	}
	return escapeService(s), nil
}

// groupID is a numeric group ID.
var groupID = regexp.MustCompile(`^[0-9]+$`)

// ValidateGroupID checks the Docker socket's group is a decimal group ID.
func ValidateGroupID(gid string) error {
	if !groupID.MatchString(gid) {
		return fmt.Errorf("the Docker socket's group %q is not a group ID, a decimal number", gid)
	}
	return nil
}

// escapeService escapes every string of s that compose interpolates. The
// values are validated before they get here; this keeps a value that slips
// past validation from being read as a variable.
func escapeService(s Service) Service {
	s.Image = escape(s.Image)
	s.Environment = escapeAll(s.Environment)
	s.Labels = escapeAll(s.Labels)
	s.Ports = escapeList(s.Ports)
	s.Volumes = escapeList(s.Volumes)
	s.GroupAdd = escapeList(s.GroupAdd)
	return s
}

func escapeList(l []string) []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l))
	for i, v := range l {
		out[i] = escape(v)
	}
	return out
}

// publish is the short-syntax port: <bind>:<host>:<container>, with an IPv6
// bind in brackets.
func publish(bind string, p catalog.Publish) string {
	host := net.JoinHostPort(bind, strconv.Itoa(p.Host))
	return host + ":" + strconv.Itoa(p.Container)
}

// escape doubles each $, which compose would otherwise read as the start of
// a variable to interpolate.
func escape(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

func escapeAll(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = escape(v)
	}
	return out
}

// YAML returns the project as a compose file.
func (p *Project) YAML() ([]byte, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return nil, fmt.Errorf("encode the compose file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode the compose file: %w", err)
	}
	return []byte(b.String()), nil
}
