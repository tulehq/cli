// Package runtime runs a rendered compose project. The commands use the
// Runtime interface; Compose implements it with the docker CLI and its
// Compose v2 plugin.
package runtime

import (
	"context"

	"tule.dev/cli/internal/render"
)

// Labels other tools set, which the runtime reads.
const (
	// LabelProject is compose's label for a container's or volume's
	// project, the stack.
	LabelProject = "com.docker.compose.project"
	// LabelVolume is compose's label for a volume's key in the compose
	// file, such as queuesdata.
	LabelVolume = "com.docker.compose.volume"
	// LabelFunctionsInstance is on every container functions starts to run
	// a function.
	LabelFunctionsInstance = "dev.tule.functions.instance"
)

// Runtime runs a stack.
type Runtime interface {
	// Check reports whether the runtime can run, naming the fix when not.
	Check(ctx context.Context) error
	// Up creates or recreates the project's services, removes the stack's
	// containers of services not in it, and waits until they are healthy.
	Up(ctx context.Context, p *render.Project) error
	// Down removes the project's containers and network, and its volumes
	// with opts.Volumes.
	Down(ctx context.Context, p *render.Project, opts DownOptions) error
	// Logs writes the logs of services, or of every service when none is
	// named.
	Logs(ctx context.Context, p *render.Project, opts LogsOptions) error
	// Restart restarts services, or every service when none is named.
	Restart(ctx context.Context, p *render.Project, services []string) error
	// PS returns every container of the stack, running or not.
	PS(ctx context.Context, stack string) ([]Container, error)
	// Volumes returns every volume of the stack.
	Volumes(ctx context.Context, stack string) ([]Volume, error)
	// RemoveVolumes deletes volumes by name, and their data.
	RemoveVolumes(ctx context.Context, names []string) error
	// RemoveFunctionContainers removes the function containers attached to
	// the stack's network, and returns how many it removed.
	RemoveFunctionContainers(ctx context.Context, stack string) (int, error)
}

// DownOptions configures Down.
type DownOptions struct {
	// Volumes also deletes the project's volumes.
	Volumes bool
}

// LogsOptions configures Logs.
type LogsOptions struct {
	// Since shows logs since a timestamp or a duration such as 10m.
	Since string
	// Services are the services to show; none is every service.
	Services []string
	// Follow keeps streaming new output.
	Follow bool
}

// Container is one container of a stack, as the daemon reports it.
type Container struct {
	// Labels are the container's labels.
	Labels map[string]string
	// Env is the container's environment, its image's included.
	Env map[string]string
	// ImageEnv is the environment of the container's image.
	ImageEnv map[string]string
	ID       string
	// Name is the container name, without the leading slash.
	Name string
	// Image is the image reference the container was created from.
	Image string
	// State is the container state: created, running, exited and so on.
	State string
	// Health is the health check status: starting, healthy or unhealthy;
	// empty when the image has no health check.
	Health string
	// Ports are the published ports.
	Ports []Port
	// Mounts are the named volumes mounted.
	Mounts []Mount
}

// Port is one published port.
type Port struct {
	HostIP    string
	HostPort  int
	Container int
}

// Mount is one named volume mounted in a container.
type Mount struct {
	// Name is the volume name, such as tule_queuesdata.
	Name string
	// Destination is where it is mounted.
	Destination string
}

// Volume is one volume of a stack.
type Volume struct {
	Labels map[string]string
	// Name is the volume name, such as tule_queuesdata.
	Name string
}

// Key is the volume's key in the compose file, such as queuesdata.
func (v Volume) Key() string {
	return v.Labels[LabelVolume]
}
