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

// State is a stack as its containers and volumes report it. Its selection
// is the catalog services that have containers, running or not.
type State struct {
	// Containers maps a service name to its container.
	Containers map[string]runtime.Container
	// configs maps each dev.tule.config value to the services whose
	// containers carry it.
	configs map[string][]string
	// Name is the stack name.
	Name string
	// Volumes are the stack's volumes, with or without containers.
	Volumes []runtime.Volume
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
// containers and volumes. Every container must be one tule created, one per
// service.
func FromContainers(
	name string,
	containers []runtime.Container,
	volumes []runtime.Volume,
) (*State, error) {
	s := &State{
		Name:       name,
		Containers: map[string]runtime.Container{},
		configs:    map[string][]string{},
		Volumes:    volumes,
	}
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
		s.configs[path] = append(s.configs[path], svc)
	}
	return s, nil
}

// Services returns the services that have containers, in catalog order.
func (s *State) Services() []string {
	var out []string
	for _, svc := range catalog.All() {
		if _, ok := s.Containers[svc.Name]; ok {
			out = append(out, svc.Name)
		}
	}
	return out
}

// Selection returns the services that have containers as a selection. It
// fails for a stack without containers, and for one left with the console
// alone.
func (s *State) Selection() (catalog.Selection, error) {
	sel, err := catalog.NewSelection(s.Services())
	if err != nil {
		return catalog.Selection{}, fmt.Errorf("stack %q: %w", s.Name, err)
	}
	return sel, nil
}

// Configs returns the config paths the stack's containers were created
// from, sorted: one for a stack tule started, none for a stack without
// containers.
func (s *State) Configs() []string {
	paths := make([]string, 0, len(s.configs))
	for path := range s.configs {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// ConfigConflictError is a stack whose containers were created from more
// than one config file.
type ConfigConflictError struct {
	// Stack is the stack name.
	Stack string
	// Parts say which services came from which file.
	Parts []string
}

// Conflict says which services came from which file, without a fix.
func (e *ConfigConflictError) Conflict() string {
	return fmt.Sprintf("stack %q was started from more than one config file: %s",
		e.Stack, strings.Join(e.Parts, "; "))
}

func (e *ConfigConflictError) Error() string {
	return e.Conflict() + "; remove it with tule down --stack " + e.Stack
}

// ConfigConflict reports containers created from more than one config file.
// Removing the whole stack and its status still work; rendering it does
// not.
func (s *State) ConfigConflict() *ConfigConflictError {
	if len(s.configs) < 2 {
		return nil
	}
	paths := s.Configs()
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		services := slices.Clone(s.configs[path])
		slices.Sort(services)
		parts = append(
			parts,
			fmt.Sprintf("%s from %s", strings.Join(services, ", "), DescribeConfig(path)),
		)
	}
	return &ConfigConflictError{Stack: s.Name, Parts: parts}
}

// DescribeConfig names a config path for a message.
func DescribeConfig(path string) string {
	if path == "" {
		return "no tule.yaml (the defaults)"
	}
	return path
}

// CheckConfig refuses to render a running stack from a config file other
// than the one it was started from, or one started from several.
func (s *State) CheckConfig(path string) error {
	if err := s.ConfigConflict(); err != nil {
		return err
	}
	paths := s.Configs()
	if len(paths) == 0 || paths[0] == path {
		return nil
	}
	started := paths[0]
	fix := "run tule where that file is found, or pass --config " + started
	if started == "" {
		fix = "run tule where no tule.yaml is found"
	}
	return fmt.Errorf(
		"stack %q was started from %s, and this run reads %s; %s, or pick another stack name with --stack",
		s.Name,
		DescribeConfig(started),
		DescribeConfig(path),
		fix,
	)
}

// VolumeNames returns the names of the stack's volumes for the catalog
// volume keys given, such as queuesdata, that exist. A volume is the
// stack's when compose made it for this project under that key: named
// <stack>_<key> and labeled with the key. Other volumes in the project are
// never named.
func (s *State) VolumeNames(keys []string) []string {
	var out []string
	for _, v := range s.Volumes {
		if slices.Contains(keys, v.Key()) && v.Name == s.Name+"_"+v.Key() {
			out = append(out, v.Name)
		}
	}
	return out
}

// CatalogVolumeKeys returns the volume key of every catalog service.
func CatalogVolumeKeys() []string {
	var out []string
	for _, svc := range catalog.All() {
		if svc.Volume != "" {
			out = append(out, svc.Volume)
		}
	}
	return out
}
