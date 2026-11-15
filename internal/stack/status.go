package stack

import (
	"net"
	"strconv"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/runtime"
)

// Docker's states.
const (
	// StateRunning is Docker's state of a running container.
	StateRunning = "running"
	// HealthHealthy is Docker's health of a container passing its checks.
	HealthHealthy = "healthy"
)

// ServiceStatus is one service of a stack.
type ServiceStatus struct {
	Name string `json:"name"`
	// State is the container's state.
	State string `json:"state"`
	// Health is the container's health check status; empty when its image
	// has none.
	Health string `json:"health"`
	// Version is its image tag.
	Version string `json:"version"`
	// Address is the host address its API is published on.
	Address string `json:"address"`
	// Volume is the volume holding its data.
	Volume string `json:"volume"`
	// Healthy is a running container whose health check, when it has one,
	// passes.
	Healthy bool `json:"healthy"`
}

// Status returns every service with a container, in catalog order.
func (s *State) Status() []ServiceStatus {
	out := []ServiceStatus{}
	for _, svc := range catalog.All() {
		c, ok := s.Containers[svc.Name]
		if !ok {
			continue
		}
		st := ServiceStatus{Name: svc.Name}
		st.fromContainer(svc, c)
		out = append(out, st)
	}
	return out
}

func (st *ServiceStatus) fromContainer(svc *catalog.Service, c runtime.Container) {
	st.State = c.State
	st.Health = c.Health
	st.Version = imageTag(c.Image)
	st.Healthy = c.State == StateRunning && (c.Health == "" || c.Health == HealthHealthy)
	for _, p := range c.Ports {
		if p.Container == svc.Ports[0].Default {
			st.Address = net.JoinHostPort(p.HostIP, strconv.Itoa(p.HostPort))
			break
		}
	}
	for _, m := range c.Mounts {
		if m.Destination == catalog.DataMount {
			st.Volume = m.Name
		}
	}
}
