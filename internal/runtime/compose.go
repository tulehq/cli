package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"tule.dev/cli/internal/render"
)

// Compose runs projects with `docker compose`, the file on standard input.
type Compose struct {
	// Out is where compose's progress and output go.
	Out io.Writer
}

// NewCompose returns a Compose runtime writing compose's output to out.
func NewCompose(out io.Writer) *Compose {
	return &Compose{Out: out}
}

// dockerBinary is the docker CLI the runtime runs.
const dockerBinary = "docker"

// Fixes named when the docker CLI or its compose plugin is missing.
const (
	installDocker  = "install Docker Engine or Docker Desktop: https://docs.docker.com/get-docker/"
	installCompose = "install the Docker Compose v2 plugin (the docker-compose-plugin package on Linux; Docker Desktop includes it): https://docs.docker.com/compose/install/"
)

// Check finds the docker CLI and the Compose v2 plugin.
func (c *Compose) Check(ctx context.Context) error {
	if _, err := exec.LookPath(dockerBinary); err != nil {
		return fmt.Errorf("the docker command is not on PATH; %s", installDocker)
	}
	out, err := c.output(ctx, "compose", "version", "--short")
	if err != nil {
		return fmt.Errorf("docker compose is not available (%w); %s", err, installCompose)
	}
	return checkComposeVersion(out)
}

// checkComposeVersion accepts Compose v2 and later. The standalone v1
// docker-compose is not a docker plugin, so `docker compose` reaching a v1
// answers only through a shim.
func checkComposeVersion(out []byte) error {
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return fmt.Errorf("docker compose version %q is not a version; %s", version, installCompose)
	}
	if major < 2 {
		return fmt.Errorf("docker compose is version %s, and tule needs v2 or later; %s",
			version, installCompose)
	}
	return nil
}

// Up runs `compose up -d --wait --remove-orphans`.
func (c *Compose) Up(ctx context.Context, p *render.Project) error {
	return c.compose(ctx, p, "up", "--detach", "--wait", "--remove-orphans")
}

// Down runs `compose down --remove-orphans`, with --volumes when asked.
func (c *Compose) Down(ctx context.Context, p *render.Project, opts DownOptions) error {
	args := []string{"down", "--remove-orphans"}
	if opts.Volumes {
		args = append(args, "--volumes")
	}
	return c.compose(ctx, p, args...)
}

// Logs runs `compose logs`.
func (c *Compose) Logs(ctx context.Context, p *render.Project, opts LogsOptions) error {
	return c.compose(ctx, p, logsArgs(opts)...)
}

func logsArgs(opts LogsOptions) []string {
	args := []string{"logs"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}
	return append(args, opts.Services...)
}

// Restart runs `compose restart`.
func (c *Compose) Restart(ctx context.Context, p *render.Project, services []string) error {
	return c.compose(ctx, p, append([]string{"restart"}, services...)...)
}

// composeArgs is the docker command line for a compose verb on p, which is
// read from standard input, with envFile as compose's only env file.
func composeArgs(p *render.Project, envFile string, verb ...string) []string {
	return append([]string{
		"compose", "--project-name", p.Name, "--file", "-", "--env-file", envFile,
	}, verb...)
}

func (c *Compose) compose(ctx context.Context, p *render.Project, verb ...string) (err error) {
	data, err := p.YAML()
	if err != nil {
		return err //nolint:wrapcheck // Already says what it failed to do.
	}
	envFile, cleanup, err := emptyEnvFile()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	//nolint:gosec // The arguments are tule's own; the stack name is validated.
	cmd := exec.CommandContext(ctx, dockerBinary, composeArgs(p, envFile, verb...)...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = c.Out
	cmd.Stderr = c.Out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", verb[0], err)
	}
	return nil
}

// emptyEnvFile makes an empty file to pass compose as --env-file, and
// returns its path and a function that removes it.
//
// Without --env-file, compose loads the .env in its working directory: it
// interpolates the compose file with it and takes COMPOSE_* settings such as
// COMPOSE_IGNORE_ORPHANS from it ("Load PWD/.env if present and no explicit
// --env-file has been set" in docker/compose cmd/compose/compose.go; the
// default lookup is in compose-go's cli.WithEnvFiles, which uses the files
// given instead of it). The file tule runs is the whole stack, so a .env
// left in the directory tule runs from must not change it. An explicit
// --env-file also takes precedence over COMPOSE_ENV_FILES.
func emptyEnvFile() (string, func() error, error) {
	dir, err := os.MkdirTemp("", "tule-compose-")
	if err != nil {
		return "", nil, fmt.Errorf("make compose's empty env file: %w", err)
	}
	cleanup := func() error {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove compose's empty env file: %w", err)
		}
		return nil
	}
	path := filepath.Join(dir, "empty.env")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return "", nil, errors.Join(fmt.Errorf("make compose's empty env file: %w", err), cleanup())
	}
	return path, cleanup, nil
}

// output runs docker with args and returns its standard output. The error
// carries its standard error.
func (c *Compose) output(ctx context.Context, args ...string) ([]byte, error) {
	//nolint:gosec // The arguments are tule's own; the stack name is validated.
	cmd := exec.CommandContext(ctx, dockerBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// ids runs a docker listing that prints one ID or name per line.
func (c *Compose) ids(ctx context.Context, args ...string) ([]string, error) {
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// PS lists the stack's containers by compose's project label, and inspects
// them and their images.
func (c *Compose) PS(ctx context.Context, stack string) ([]Container, error) {
	ids, err := c.ids(ctx, "ps", "--all", "--quiet", "--no-trunc",
		"--filter", "label="+LabelProject+"="+stack)
	if err != nil {
		return nil, fmt.Errorf("list the stack's containers: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := c.output(ctx, append([]string{"container", "inspect"}, ids...)...)
	if err != nil {
		return nil, fmt.Errorf("inspect the stack's containers: %w", err)
	}
	containers, imageIDs, err := parseContainers(out)
	if err != nil {
		return nil, err
	}
	distinct := slices.Compact(slices.Sorted(slices.Values(imageIDs)))
	out, err = c.output(ctx, append([]string{"image", "inspect"}, distinct...)...)
	if err != nil {
		return nil, fmt.Errorf("inspect the stack's images: %w", err)
	}
	imageEnv, err := parseImages(out)
	if err != nil {
		return nil, err
	}
	for i := range containers {
		env, ok := imageEnv[imageIDs[i]]
		if !ok {
			return nil, fmt.Errorf("container %s: docker image inspect did not report its image %s",
				containers[i].Name, imageIDs[i])
		}
		containers[i].ImageEnv = env
	}
	return containers, nil
}

// containerJSON is the part of `docker container inspect` PS reads.
type containerJSON struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	State struct {
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
		Status string `json:"Status"`
	} `json:"State"`
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// parseContainers reads `docker container inspect` output, and returns the
// containers and, by index, the IDs of their images.
func parseContainers(data []byte) ([]Container, []string, error) {
	var raw []containerJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse docker container inspect: %w", err)
	}
	out := make([]Container, 0, len(raw))
	images := make([]string, 0, len(raw))
	for _, r := range raw {
		ct := Container{
			ID:     r.ID,
			Name:   strings.TrimPrefix(r.Name, "/"),
			Image:  r.Config.Image,
			Labels: r.Config.Labels,
			Env:    envMap(r.Config.Env),
			State:  r.State.Status,
		}
		if r.State.Health != nil {
			ct.Health = r.State.Health.Status
		}
		for spec, bindings := range r.NetworkSettings.Ports {
			port, err := containerPort(spec)
			if err != nil {
				return nil, nil, err
			}
			for _, b := range bindings {
				host, err := strconv.Atoi(b.HostPort)
				if err != nil {
					return nil, nil, fmt.Errorf(
						"container %s: host port %q: %w",
						ct.Name,
						b.HostPort,
						err,
					)
				}
				ct.Ports = append(ct.Ports, Port{HostIP: b.HostIP, HostPort: host, Container: port})
			}
		}
		slices.SortFunc(ct.Ports, cmpPorts)
		for _, m := range r.Mounts {
			if m.Type == "volume" {
				ct.Mounts = append(ct.Mounts, Mount{Name: m.Name, Destination: m.Destination})
			}
		}
		images = append(images, r.Image)
		out = append(out, ct)
	}
	return out, images, nil
}

func cmpPorts(a, b Port) int {
	if a.Container != b.Container {
		return a.Container - b.Container
	}
	if a.HostPort != b.HostPort {
		return a.HostPort - b.HostPort
	}
	return strings.Compare(a.HostIP, b.HostIP)
}

// containerPort reads a port key such as 9000/tcp.
func containerPort(spec string) (int, error) {
	number, _, _ := strings.Cut(spec, "/")
	port, err := strconv.Atoi(number)
	if err != nil {
		return 0, fmt.Errorf("container port %q: %w", spec, err)
	}
	return port, nil
}

// parseImages reads `docker image inspect` output into each image's
// environment by image ID.
func parseImages(data []byte) (map[string]map[string]string, error) {
	var raw []struct {
		ID     string `json:"Id"`
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse docker image inspect: %w", err)
	}
	out := make(map[string]map[string]string, len(raw))
	for _, r := range raw {
		out[r.ID] = envMap(r.Config.Env)
	}
	return out, nil
}

// envMap reads NAME=value pairs.
func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// Volumes lists the stack's volumes by compose's project label.
func (c *Compose) Volumes(ctx context.Context, stack string) ([]Volume, error) {
	names, err := c.ids(ctx, "volume", "ls", "--quiet",
		"--filter", "label="+LabelProject+"="+stack)
	if err != nil {
		return nil, fmt.Errorf("list the stack's volumes: %w", err)
	}
	if len(names) == 0 {
		return nil, nil
	}
	out, err := c.output(ctx, append([]string{"volume", "inspect"}, names...)...)
	if err != nil {
		return nil, fmt.Errorf("inspect the stack's volumes: %w", err)
	}
	return parseVolumes(out)
}

func parseVolumes(data []byte) ([]Volume, error) {
	var raw []struct {
		Labels map[string]string `json:"Labels"`
		Name   string            `json:"Name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse docker volume inspect: %w", err)
	}
	out := make([]Volume, 0, len(raw))
	for _, r := range raw {
		out = append(out, Volume{Name: r.Name, Labels: r.Labels})
	}
	return out, nil
}

// RemoveVolumes runs `docker volume rm`.
func (c *Compose) RemoveVolumes(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if _, err := c.output(ctx, append([]string{"volume", "rm"}, names...)...); err != nil {
		return fmt.Errorf("remove volumes: %w", err)
	}
	return nil
}

// NetworkName is the network compose makes for a stack.
func NetworkName(stack string) string {
	return stack + "_default"
}

// RemoveFunctionContainers removes the containers labeled as functions'
// on the stack's network. Function containers of other stacks, on their own
// networks, are left alone.
func (c *Compose) RemoveFunctionContainers(ctx context.Context, stack string) (int, error) {
	ids, err := c.ids(ctx, "ps", "--all", "--quiet", "--no-trunc",
		"--filter", "label="+LabelFunctionsInstance,
		"--filter", "network="+NetworkName(stack))
	if err != nil {
		return 0, fmt.Errorf("list function containers: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := c.output(
		ctx,
		append([]string{"container", "rm", "--force"}, ids...)...); err != nil {
		return 0, fmt.Errorf("remove function containers: %w", err)
	}
	return len(ids), nil
}
