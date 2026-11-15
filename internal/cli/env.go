package cli

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
)

// Shells `tule env` writes for.
const (
	shellSh         = "sh"
	shellFish       = "fish"
	shellPowerShell = "powershell"
)

// variable is one environment variable to set.
type variable struct {
	name  string
	value string
}

// sessionToken is unset, so a token left from a real AWS session is not sent
// with the stack's key.
const sessionToken = "AWS_SESSION_TOKEN" //nolint:gosec // A variable name, not a credential.

func newEnvCommand(g *globals) *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print the variables that point AWS SDKs and the AWS CLI at the stack",
		Long: "Print the credentials, region and per-service endpoint variables for the\n" +
			"stack's services, as commands for a shell:\n\n" +
			"  eval \"$(tule env)\"\n" +
			"  tule env --shell fish | source\n" +
			"  tule env --shell powershell | Out-String | Invoke-Expression\n\n" +
			"A running stack is read back from its containers: their services, host ports and\n" +
			"root key, so it works from any directory. Without one, the variables are those\n" +
			"of the configured services. Standard error says which.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.loadConfig(cmd)
			if err != nil {
				return err
			}
			rt, err := g.runtime(cmd)
			if err != nil {
				return err
			}
			st, err := g.readState(cmd, rt)
			if err != nil {
				return err
			}
			var src envSource
			if st.Running() {
				src, err = stateEnvSource(cmd, st, cfg)
			} else {
				src, err = configEnvSource(cmd, g.stack, cfg)
			}
			if err != nil {
				return err
			}
			return writeEnv(cmd.OutOrStdout(), shell, envVariables(src))
		},
	}
	cmd.Flags().
		StringVar(&shell, "shell", shellSh, "the shell to print for: sh, fish or powershell")
	return cmd
}

// envSource is what the variables are made of.
type envSource struct {
	// apiPorts maps each service to the host port of its API.
	apiPorts map[string]int
	rootKey  catalog.RootKey
	// publicHost is the host the endpoints name.
	publicHost string
	services   []*catalog.Service
}

// configEnvSource is the configured services, ports and key, for a stack
// that is not running.
func configEnvSource(cmd *cobra.Command, name string, cfg *config.Config) (envSource, error) {
	sel, err := cfg.Selection(nil)
	if err != nil {
		return envSource{}, fmt.Errorf("env: %w", err)
	}
	src, err := configSource(cfg, sel)
	if err != nil {
		return envSource{}, err
	}
	return src, note(
		cmd,
		fmt.Sprintf("stack %s is not running; the variables are for %s's services: %s",
			name, stack.DescribeConfig(cfg.Path), sel),
	)
}

// configSource is the services of sel with the ports and key of cfg.
func configSource(cfg *config.Config, sel catalog.Selection) (envSource, error) {
	hostPorts, err := cfg.HostPorts(sel)
	if err != nil {
		return envSource{}, fmt.Errorf("host ports: %w", err)
	}
	src := envSource{
		apiPorts:   map[string]int{},
		rootKey:    cfg.RootKey,
		publicHost: cfg.PublicHost,
		services:   sel.Services(),
	}
	for _, svc := range src.services {
		src.apiPorts[svc.Name] = hostPorts.Of(svc.Name, catalog.PortAPI)
	}
	return src, nil
}

// Variables of the root key on the services' containers.
const (
	rootKeyIDVar     = "TULE_Auth_AccessKeyID"
	rootKeySecretVar = "TULE_Auth_SecretAccessKey" //nolint:gosec // A variable name, not a credential.
)

// stateEnvSource reads the services, their host ports and the root key back
// from a running stack's containers. The endpoints name publicHost from
// cfg when it is the tule.yaml the stack was started from; otherwise
// localhost for a stack published on a loopback or every address, and the
// address it is published on else.
func stateEnvSource(cmd *cobra.Command, st *stack.State, cfg *config.Config) (envSource, error) {
	key, err := readBackKey(st)
	if err != nil {
		return envSource{}, err
	}
	src := envSource{apiPorts: map[string]int{}, rootKey: key}
	var bindIP string
	names := []string{}
	for _, name := range st.Services() {
		svc := mustLookup(name)
		c := st.Containers[name]
		port, ip, ok := apiBinding(c, svc)
		if !ok {
			if err := warn(cmd, fmt.Sprintf(
				"%s publishes no API port (its container is %s); it has no endpoint variables",
				name, c.State)); err != nil {
				return envSource{}, err
			}
			continue
		}
		src.services = append(src.services, svc)
		src.apiPorts[name] = port
		names = append(names, name)
		bindIP = ip
	}
	src.publicHost = readBackHost(bindIP)
	if started := st.Configs(); len(started) == 1 && started[0] == cfg.Path {
		src.publicHost = cfg.PublicHost
	}
	return src, note(cmd, fmt.Sprintf("read back from running stack %s: %s",
		st.Name, strings.Join(names, ",")))
}

// readBackKey is the root key the stack's services were started with. They
// must agree.
func readBackKey(st *stack.State) (catalog.RootKey, error) {
	var key catalog.RootKey
	var from string
	for _, name := range st.Services() {
		env := st.Containers[name].Env
		id, ok := env[rootKeyIDVar]
		if !ok {
			continue
		}
		k := catalog.RootKey{ID: id, Secret: env[rootKeySecretVar]}
		if from != "" && k != key {
			return catalog.RootKey{}, fmt.Errorf(
				"stack %q: %s and %s have different root keys", st.Name, from, name)
		}
		key, from = k, name
	}
	if from == "" {
		return catalog.RootKey{}, fmt.Errorf(
			"stack %q runs no service with the root key to read back", st.Name)
	}
	return key, nil
}

// apiBinding returns the host port and address a container publishes its
// service's API on.
func apiBinding(c runtime.Container, svc *catalog.Service) (port int, ip string, ok bool) {
	for _, p := range c.Ports {
		if p.Container == svc.Ports[0].Default {
			return p.HostPort, p.HostIP, true
		}
	}
	return 0, "", false
}

// readBackHost is the host clients on this machine reach a port published
// on ip at.
func readBackHost(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified() {
		return config.DefaultPublicHost
	}
	return ip
}

// note prints a line about where the output came from to standard error.
func note(cmd *cobra.Command, msg string) error {
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "tule env: "+msg); err != nil {
		return fmt.Errorf("print a note: %w", err)
	}
	return nil
}

// envVariables returns the credentials, the region, and an endpoint for each
// variable of each service.
//
// The endpoint variable names are the AWS_ENDPOINT_URL_<SERVICE> names in
// the AWS SDKs and Tools Reference Guide's "Identifiers for service-specific
// endpoints" table:
// https://docs.aws.amazon.com/sdkref/latest/guide/ss-endpoints-table.html
func envVariables(src envSource) []variable {
	vars := []variable{
		{"AWS_ACCESS_KEY_ID", src.rootKey.ID},
		{"AWS_SECRET_ACCESS_KEY", src.rootKey.Secret},
		{"AWS_REGION", catalog.Region},
		// boto3 and the AWS CLI v1 read only this one.
		{"AWS_DEFAULT_REGION", catalog.Region},
	}
	for _, svc := range src.services {
		port := strconv.Itoa(src.apiPorts[svc.Name])
		for _, suffix := range svc.EndpointVars {
			host := src.publicHost
			if suffix == "S3" {
				host = s3Host(src.publicHost)
			}
			vars = append(vars, variable{
				"AWS_ENDPOINT_URL_" + suffix,
				"http://" + net.JoinHostPort(host, port),
			})
		}
	}
	return vars
}

// s3Host is the host of the S3 endpoint. SDK endpoint rules address buckets
// path style at an IP address and virtual-hosted style at a host name, which
// needs <bucket>.<host> to resolve; so localhost becomes 127.0.0.1, and an IP
// address or another host name is kept.
func s3Host(publicHost string) string {
	if publicHost == "localhost" {
		return "127.0.0.1"
	}
	return publicHost
}

// writeEnv prints vars as commands for shell: an assignment of each, and an
// unset of the session token. A value holding a newline or another control
// character is refused, in every shell: each command is one line, and
// PowerShell's Invoke-Expression, given the output without Out-String, runs
// it line by line.
func writeEnv(w io.Writer, shell string, vars []variable) error {
	var set func(variable) string
	var unset string
	switch shell {
	case shellSh:
		set = func(v variable) string { return "export " + v.name + "=" + quoteSh(v.value) }
		unset = "unset " + sessionToken
	case shellFish:
		set = func(v variable) string { return "set -gx " + v.name + " " + quoteFish(v.value) }
		unset = "set -e " + sessionToken
	case shellPowerShell:
		set = func(v variable) string { return "$env:" + v.name + " = " + quotePowerShell(v.value) }
		unset = "Remove-Item Env:" + sessionToken + " -ErrorAction SilentlyContinue"
	default:
		return fmt.Errorf("--shell %q: use sh, fish or powershell", shell)
	}
	var b strings.Builder
	for _, v := range vars {
		if i := strings.IndexFunc(v.value, unicode.IsControl); i >= 0 {
			return fmt.Errorf("%s holds a control character, %U, which tule env does not print",
				v.name, []rune(v.value[i:])[0])
		}
		b.WriteString(set(v) + "\n")
	}
	b.WriteString(unset + "\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("print the variables: %w", err)
	}
	return nil
}

// quoteSh single-quotes s. POSIX sh reads nothing inside single quotes as
// special but the closing quote, so a single quote inside ends the quoting,
// is escaped, and starts it again.
func quoteSh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteFish single-quotes s; inside, fish reads only \\ and \' as escapes.
func quoteFish(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// powerShellQuotes are the characters PowerShell reads as a single quote:
// the ASCII one and the four typographic ones (U+2018, U+2019, U+201A,
// U+201B).
const powerShellQuotes = "'\u2018\u2019\u201a\u201b"

// quotePowerShell single-quotes s; inside, each character PowerShell reads
// as a single quote is doubled, as its CodeGeneration.
// EscapeSingleQuotedStringContent does.
func quotePowerShell(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		if strings.ContainsRune(powerShellQuotes, r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}
