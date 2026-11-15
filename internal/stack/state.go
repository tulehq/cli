// Package stack reads a running stack back from its containers, plans what
// a new render changes in it, and checks the host ports it needs are free.
// No state is kept on disk: the containers' labels are the state.
package stack

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
)

// State is a stack as its containers and volumes report it.
type State struct {
	// Containers maps a service name to its container.
	Containers map[string]runtime.Container
	// Name is the stack name.
	Name string
	// ConfigPath is the absolute path of the tule.yaml the stack was
	// started from, or empty when it was started with none.
	ConfigPath string
	// Warnings are worth saying before acting on the state.
	Warnings []string
	// Volumes are the stack's volumes, with or without containers.
	Volumes []runtime.Volume
	// Selection is the stack's selection; zero when it has no containers.
	Selection catalog.Selection
}

// Read reads the stack named name back from the runtime.
func Read(ctx context.Context, rt runtime.Runtime, name string) (*State, error) {
	containers, err := rt.PS(ctx, name)
	if err != nil {
		return nil, err //nolint:wrapcheck // The runtime says what failed.
	}
	volumes, err := rt.Volumes(ctx, name)
	if err != nil {
		return nil, err //nolint:wrapcheck // The runtime says what failed.
	}
	return FromContainers(name, containers, volumes)
}

// Running reports whether the stack has containers.
func (s *State) Running() bool {
	return len(s.Containers) > 0
}

// FromContainers builds the state of the stack named name from its
// containers and volumes. Every container must be one tule created, and
// all must name the same config file. The selection is the one the
// containers' labels agree on; when they disagree, as an interrupted up or
// down leaves them, it is the services that have containers, with a
// warning.
func FromContainers(
	name string,
	containers []runtime.Container,
	volumes []runtime.Volume,
) (*State, error) {
	s := &State{Name: name, Containers: map[string]runtime.Container{}, Volumes: volumes}
	if len(containers) == 0 {
		return s, nil
	}
	configs := map[string][]string{}
	selections := map[string]bool{}
	for _, c := range containers {
		svc := c.Labels[render.LabelService]
		if _, err := catalog.Lookup(svc); err != nil {
			return nil, fmt.Errorf(
				"stack %q has container %s, which tule did not create; pick another stack name with --stack",
				name,
				c.Name,
			)
		}
		if other, ok := s.Containers[svc]; ok {
			return nil, fmt.Errorf("stack %q has two containers for %s: %s and %s",
				name, svc, other.Name, c.Name)
		}
		s.Containers[svc] = c
		path := c.Labels[render.LabelConfig]
		configs[path] = append(configs[path], svc)
		selections[c.Labels[render.LabelSelection]] = true
	}
	if len(configs) > 1 {
		return nil, configsDisagree(name, configs)
	}
	for path := range configs {
		s.ConfigPath = path
	}
	sel, err := s.agreedSelection(selections)
	if err != nil {
		return nil, err
	}
	s.Selection = sel
	return s, nil
}

func (s *State) agreedSelection(labels map[string]bool) (catalog.Selection, error) {
	if len(labels) == 1 {
		for label := range labels {
			sel, err := catalog.NewSelection(strings.Split(label, ","))
			if err == nil {
				return sel, nil
			}
		}
	}
	running := make([]string, 0, len(s.Containers))
	for svc := range s.Containers {
		running = append(running, svc)
	}
	sel, err := catalog.NewSelection(running)
	if err != nil {
		return catalog.Selection{}, fmt.Errorf("stack %q: its containers are %s: %w",
			s.Name, strings.Join(running, ", "), err)
	}
	s.Warnings = append(s.Warnings, fmt.Sprintf(
		"stack %q's containers do not name one selection, as an interrupted up or down leaves them; taking the services that have containers: %s",
		s.Name,
		sel,
	))
	return sel, nil
}

func configsDisagree(name string, configs map[string][]string) error {
	paths := make([]string, 0, len(configs))
	for path := range configs {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		services := configs[path]
		slices.Sort(services)
		parts = append(
			parts,
			fmt.Sprintf("%s from %s", strings.Join(services, ", "), DescribeConfig(path)),
		)
	}
	return fmt.Errorf(
		"stack %q was started from more than one config file: %s; remove it with tule down --stack %s",
		name,
		strings.Join(parts, "; "),
		name,
	)
}

// DescribeConfig names a config path for a message.
func DescribeConfig(path string) string {
	if path == "" {
		return "no tule.yaml (the defaults)"
	}
	return path
}

// CheckConfig refuses to act on a running stack with a config file other
// than the one it was started from.
func (s *State) CheckConfig(path string) error {
	if !s.Running() || s.ConfigPath == path {
		return nil
	}
	fix := "run tule where that file is found, or pass --config " + s.ConfigPath
	if s.ConfigPath == "" {
		fix = "run tule where no tule.yaml is found"
	}
	return fmt.Errorf(
		"stack %q was started from %s, and this run reads %s; %s, or pick another stack name with --stack",
		s.Name,
		DescribeConfig(s.ConfigPath),
		DescribeConfig(path),
		fix,
	)
}

// VolumeVersions maps the key of each of the stack's volumes to the version
// recorded on it at creation.
func (s *State) VolumeVersions() map[string]string {
	out := map[string]string{}
	for _, v := range s.Volumes {
		version, ok := v.Labels[render.LabelVersion]
		if ok && v.Key() != "" {
			out[v.Key()] = version
		}
	}
	return out
}

// VolumeNames returns the names of the stack's volumes whose keys are
// keys.
func (s *State) VolumeNames(keys []string) []string {
	var out []string
	for _, v := range s.Volumes {
		if slices.Contains(keys, v.Key()) {
			out = append(out, v.Name)
		}
	}
	return out
}
