// Package config reads tule.yaml, the stack's configuration, and fills in
// the defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"tule.dev/cli/internal/catalog"
)

// FileName is the configuration file looked for in the working directory
// and its parents.
const FileName = "tule.yaml"

// Defaults.
const (
	DefaultBind       = "127.0.0.1"
	DefaultPublicHost = "localhost"
)

// file is tule.yaml as written.
type file struct {
	Services   *[]string                    `yaml:"services"`
	Ports      map[string]int               `yaml:"ports"`
	Versions   map[string]string            `yaml:"versions"`
	RootKey    *rootKey                     `yaml:"rootKey"`
	Env        map[string]map[string]string `yaml:"env"`
	Bind       string                       `yaml:"bind"`
	PublicHost string                       `yaml:"publicHost"`
	PortOffset int                          `yaml:"portOffset"`
}

type rootKey struct {
	ID     string `yaml:"id"`
	Secret string `yaml:"secret"`
}

// Config is the configuration of a stack, defaults filled in.
type Config struct {
	// Ports overrides services' API host ports.
	Ports map[string]int
	// Versions overrides catalog versions.
	Versions map[string]string
	// Env is extra environment per service, set over the wiring.
	Env map[string]map[string]string
	// Path is the absolute path of the tule.yaml read, or empty when there
	// was none.
	Path    string
	RootKey catalog.RootKey
	// Bind is the address host ports are published on.
	Bind string
	// PublicHost is the host clients on this machine reach the services at.
	PublicHost string
	// Services is the configured selection, or nil for the default set.
	Services []string
	// PortOffset is added to every default host port.
	PortOffset int
}

// Options says where the configuration comes from.
type Options struct {
	// Dir is where the search for tule.yaml starts, walking up.
	Dir string
	// File, when set, is the configuration file to read instead (the
	// --config flag); it must exist.
	File string
}

// Load reads the configuration and validates it. Warnings are returned for
// settings that are allowed but worth saying, such as a version that differs
// from the tested one.
func Load(opts Options) (*Config, []string, error) {
	path, err := locate(opts)
	if err != nil {
		return nil, nil, err
	}
	if path == "" {
		return Parse(nil)
	}
	data, err := os.ReadFile(path) //nolint:gosec // The path is the configuration file asked for.
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	cfg, warnings, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	return cfg, warnings, nil
}

// Parse validates a tule.yaml document and fills in the defaults; empty data
// is all defaults. The returned Config has no Path.
func Parse(data []byte) (*Config, []string, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	return fromFile(f)
}

// locate returns the absolute path of the configuration file, or empty when
// none was named and none was found.
func locate(opts Options) (string, error) {
	if opts.File != "" {
		abs, err := filepath.Abs(opts.File)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", opts.File, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("config file: %w", err)
		}
		return abs, nil
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", opts.Dir, err)
	}
	for {
		candidate := filepath.Join(dir, FileName)
		_, err := os.Stat(candidate)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("look for %s: %w", FileName, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// validated checks the key holds only keyPattern's characters.
func (k *rootKey) validated() (catalog.RootKey, error) {
	if k.ID == "" || k.Secret == "" {
		return catalog.RootKey{}, errors.New("rootKey: needs both id and secret")
	}
	if !keyPattern.MatchString(k.ID) || !keyPattern.MatchString(k.Secret) {
		return catalog.RootKey{}, errors.New(
			"rootKey: id and secret may hold only letters, digits and + / = . _ -")
	}
	return catalog.RootKey{ID: k.ID, Secret: k.Secret}, nil
}

// fromFile validates f and fills in the defaults.
func fromFile(f file) (*Config, []string, error) {
	cfg := &Config{
		Ports:      f.Ports,
		PortOffset: f.PortOffset,
		Versions:   f.Versions,
		RootKey: catalog.RootKey{
			ID:     catalog.ExampleAccessKeyID,
			Secret: catalog.ExampleSecretAccessKey,
		},
		Bind:       DefaultBind,
		PublicHost: DefaultPublicHost,
		Env:        f.Env,
	}
	if f.Services != nil {
		if len(*f.Services) == 0 {
			return nil, nil, errors.New(
				"services: is empty; list at least one service, or leave it out for the default set",
			)
		}
		cfg.Services = *f.Services
		if _, err := catalog.NewSelection(cfg.Services); err != nil {
			return nil, nil, fmt.Errorf("services: %w", err)
		}
	}
	if f.RootKey != nil {
		key, err := f.RootKey.validated()
		if err != nil {
			return nil, nil, err
		}
		cfg.RootKey = key
	}
	if f.Bind != "" {
		cfg.Bind = f.Bind
	}
	if f.PublicHost != "" {
		cfg.PublicHost = f.PublicHost
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	return cfg, cfg.versionWarnings(), nil
}

// Validate checks the configuration, the bind guard included. Parse runs it;
// the render runs it again on the configuration it is given.
func (c *Config) Validate() error {
	if err := knownKeys("ports", c.Ports); err != nil {
		return err
	}
	if err := knownKeys("versions", c.Versions); err != nil {
		return err
	}
	if err := knownKeys("env", c.Env); err != nil {
		return err
	}
	if err := c.validatePorts(); err != nil {
		return err
	}
	if err := c.validateValues(); err != nil {
		return err
	}
	return c.validateAddresses()
}

func (c *Config) validatePorts() error {
	for name, port := range c.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("ports: %s: %d is not a port", name, port)
		}
	}
	if c.PortOffset < 0 {
		return fmt.Errorf("portOffset: %d is negative", c.PortOffset)
	}
	for _, svc := range catalog.All() {
		for _, p := range svc.Ports {
			if p.Default+c.PortOffset > 65535 {
				return fmt.Errorf("portOffset: %s's %s port %d+%d is past 65535",
					svc.Name, p.Name, p.Default, c.PortOffset)
			}
		}
	}
	return nil
}

func (c *Config) validateValues() error {
	for name, v := range c.Versions {
		if !versionPattern.MatchString(v) {
			return fmt.Errorf(
				"versions: %s: %q is not a release version, such as 0.13.0 or 0.14.0-rc.1", name, v)
		}
	}
	for name, vars := range c.Env {
		for k := range vars {
			if k == "" || strings.ContainsAny(k, "= \t\n") {
				return fmt.Errorf("env: %s: %q is not a variable name", name, k)
			}
			// Variable names are matched ignoring case, as the services
			// read them.
			if strings.EqualFold(k, rootKeyIDVar) || strings.EqualFold(k, rootKeySecretVar) {
				return fmt.Errorf("env: %s: %s is the root key; set it with rootKey", name, k)
			}
		}
	}
	return nil
}

// versionPattern is a release version: digits.digits.digits with an
// optional prerelease. An image tag reaches compose, which would read
// anything else, such as ${HOME}, as something to interpolate.
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// The variables the root key is configured with. They are set from rootKey
// only, so the bind guard sees the key every service gets.
const (
	rootKeyIDVar     = "TULE_Auth_AccessKeyID"
	rootKeySecretVar = "TULE_Auth_SecretAccessKey" //nolint:gosec // A variable name, not a credential.
)

func (c *Config) validateAddresses() error {
	bind := net.ParseIP(c.Bind)
	if bind == nil {
		return fmt.Errorf("bind: %q is not an IP address", c.Bind)
	}
	example := c.RootKey.ID == catalog.ExampleAccessKeyID ||
		c.RootKey.Secret == catalog.ExampleSecretAccessKey
	if !bind.IsLoopback() && example {
		return fmt.Errorf(
			"bind: %s is reachable from other machines, and rootKey's id or secret is from the AWS documentation's example key; set rootKey to a key of your own",
			c.Bind,
		)
	}
	if !isHostName(c.PublicHost) && net.ParseIP(c.PublicHost) == nil {
		return fmt.Errorf("publicHost: %q is not a host name or IP address", c.PublicHost)
	}
	return nil
}

// keyPattern is what a root key's ID and secret may hold: the characters of
// AWS access key IDs and secrets, and nothing a shell or compose reads as
// special. `tule env` prints the key for a shell to evaluate.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9+/=._-]+$`)

// hostLabel is one label of a host name (RFC 1123).
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func isHostName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(s, "."), ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// versionWarnings names each version override that differs from the
// catalog's tested version, in catalog order.
func (c *Config) versionWarnings() []string {
	var out []string
	for _, svc := range catalog.All() {
		v, ok := c.Versions[svc.Name]
		if ok && v != svc.Version {
			out = append(out, fmt.Sprintf(
				"versions: %s %s is not the tested version, %s", svc.Name, v, svc.Version))
		}
	}
	return out
}

func knownKeys[V any](key string, m map[string]V) error {
	for name := range m {
		if _, err := catalog.Lookup(name); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// Selection returns the services named, or, with none named, the configured
// services, or the default set.
func (c *Config) Selection(names []string) (catalog.Selection, error) {
	switch {
	case len(names) > 0:
		return newSelection(names)
	case c.Services != nil:
		return newSelection(c.Services)
	default:
		return catalog.DefaultSelection(), nil
	}
}

func newSelection(names []string) (catalog.Selection, error) {
	sel, err := catalog.NewSelection(names)
	if err != nil {
		return catalog.Selection{}, fmt.Errorf("select services: %w", err)
	}
	return sel, nil
}

// Version returns the version to run of the service named name.
func (c *Config) Version(svc *catalog.Service) string {
	if v, ok := c.Versions[svc.Name]; ok {
		return v
	}
	return svc.Version
}

// HostPorts returns the host ports of the selected services: each port's
// default plus the offset, or for the API port, its entry in ports. Two
// ports on the same number are an error.
func (c *Config) HostPorts(sel catalog.Selection) (catalog.HostPorts, error) {
	out := catalog.HostPorts{}
	used := map[int]string{}
	for _, svc := range sel.Services() {
		ports := map[string]int{}
		for i, p := range svc.Ports {
			port := p.Default + c.PortOffset
			if override, ok := c.Ports[svc.Name]; ok && i == 0 {
				port = override
			}
			owner := svc.Name + " " + p.Name
			if other, ok := used[port]; ok {
				return nil, fmt.Errorf(
					"ports: %s and %s are both on host port %d",
					other,
					owner,
					port,
				)
			}
			used[port] = owner
			ports[p.Name] = port
		}
		out[svc.Name] = ports
	}
	return out, nil
}
