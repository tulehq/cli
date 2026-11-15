package stack

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
)

// Binding is one host port a service is published on.
type Binding struct {
	Service string
	// Port is the port's name, catalog.PortAPI.
	Port      string
	HostIP    string
	HostPort  int
	Container int
}

// Bindings returns the host ports of the selected services, in catalog
// order.
func Bindings(cfg *config.Config, sel catalog.Selection) ([]Binding, error) {
	hostPorts, err := cfg.HostPorts(sel)
	if err != nil {
		return nil, fmt.Errorf("host ports: %w", err)
	}
	var out []Binding
	for _, svc := range sel.Services() {
		// A service publishes its ports in the order it lists them.
		pubs := svc.Publishes(hostPorts[svc.Name])
		if len(pubs) != len(svc.Ports) {
			return nil, fmt.Errorf(
				"%s publishes %d ports and lists %d",
				svc.Name,
				len(pubs),
				len(svc.Ports),
			)
		}
		for i, pub := range pubs {
			out = append(out, Binding{
				Service:   svc.Name,
				Port:      svc.Ports[i].Name,
				HostIP:    cfg.Bind,
				HostPort:  pub.Host,
				Container: pub.Container,
			})
		}
	}
	return out, nil
}

// Held reports whether a host port is published by one of the stack's own
// containers, which compose frees when it recreates or removes them.
func (s *State) Held(ip string, port int) bool {
	for _, c := range s.Containers {
		for _, p := range c.Ports {
			if p.HostPort == port && sameIP(p.HostIP, ip) {
				return true
			}
		}
	}
	return false
}

func sameIP(a, b string) bool {
	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	return ipA != nil && ipB != nil && ipA.Equal(ipB)
}

// Preflight checks every binding the stack does not already hold is free
// to listen on, and names the service and the tule.yaml keys that move it
// when one is not. The check listens on this machine, so it is right for a
// local Docker daemon, Docker Desktop included.
func Preflight(ctx context.Context, bindings []Binding, held func(ip string, port int) bool) error {
	var conflicts []string
	for _, b := range bindings {
		if held(b.HostIP, b.HostPort) {
			continue
		}
		addr := net.JoinHostPort(b.HostIP, strconv.Itoa(b.HostPort))
		var lc net.ListenConfig
		l, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			conflicts = append(conflicts, fmt.Sprintf(
				"%s's %s port %s is not free (%v); %s", b.Service, b.Port, addr, err, moveHint(b)))
			continue
		}
		if err := l.Close(); err != nil {
			return fmt.Errorf("close the probe of %s: %w", addr, err)
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("host ports in use:\n  %s", strings.Join(conflicts, "\n  "))
	}
	return nil
}

// moveHint names the tule.yaml keys that move a binding.
func moveHint(b Binding) string {
	return fmt.Sprintf(
		"set another in tule.yaml with ports: {%s: <port>}, or move every port with portOffset:",
		b.Service,
	)
}
