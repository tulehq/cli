// Package stacktest makes the containers compose would run for a rendered
// project, for tests that read a stack back without a Docker daemon.
package stacktest

import (
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
)

// ImageEnv is the environment every test image sets.
var ImageEnv = map[string]string{"PATH": "/usr/local/bin:/usr/bin"}

// Containers returns a running, healthy container for each service of p,
// in name order, as docker inspect reports what compose creates.
func Containers(p *render.Project) []runtime.Container {
	out := make([]runtime.Container, 0, len(p.Services))
	for _, name := range slices.Sorted(maps.Keys(p.Services)) {
		svc := p.Services[name]
		env := maps.Clone(ImageEnv)
		for k, v := range svc.Environment {
			env[k] = unescape(v)
		}
		labels := map[string]string{runtime.LabelProject: p.Name}
		for k, v := range svc.Labels {
			labels[k] = unescape(v)
		}
		c := runtime.Container{
			ID:       p.Name + "-" + name,
			Name:     p.Name + "-" + name + "-1",
			Image:    unescape(svc.Image),
			State:    "running",
			Health:   "healthy",
			Labels:   labels,
			Env:      env,
			ImageEnv: maps.Clone(ImageEnv),
		}
		for _, g := range svc.GroupAdd {
			c.GroupAdd = append(c.GroupAdd, unescape(g))
		}
		for _, o := range svc.SecurityOpt {
			c.SecurityOpt = append(c.SecurityOpt, unescape(o))
		}
		for _, spec := range svc.Ports {
			c.Ports = append(c.Ports, port(spec))
		}
		for _, v := range svc.Volumes {
			source, dest, _ := strings.Cut(v, ":")
			if !strings.HasPrefix(source, "/") {
				c.Mounts = append(
					c.Mounts,
					runtime.Mount{Name: p.Name + "_" + source, Destination: dest},
				)
			}
		}
		out = append(out, c)
	}
	return out
}

// Volumes returns the volumes compose creates for p.
func Volumes(p *render.Project) []runtime.Volume {
	out := make([]runtime.Volume, 0, len(p.Volumes))
	for _, key := range slices.Sorted(maps.Keys(p.Volumes)) {
		labels := map[string]string{runtime.LabelProject: p.Name, runtime.LabelVolume: key}
		out = append(out, runtime.Volume{Name: p.Name + "_" + key, Labels: labels})
	}
	return out
}

// port reads a short-syntax port, <ip>:<host>:<container>.
func port(spec string) runtime.Port {
	i := strings.LastIndex(spec, ":")
	host, hostPort, err := net.SplitHostPort(spec[:i])
	if err != nil {
		panic(err)
	}
	return runtime.Port{HostIP: host, HostPort: atoi(hostPort), Container: atoi(spec[i+1:])}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

func unescape(s string) string {
	return strings.ReplaceAll(s, "$$", "$")
}

// Lookup returns the catalog service named name, panicking when it is not
// one.
func Lookup(name string) *catalog.Service {
	svc, err := catalog.Lookup(name)
	if err != nil {
		panic(err)
	}
	return svc
}
