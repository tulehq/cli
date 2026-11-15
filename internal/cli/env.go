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
			"configured services, as commands for a shell:\n\n" +
			"  eval \"$(tule env)\"\n" +
			"  tule env --shell fish | source\n" +
			"  tule env --shell powershell | Out-String | Invoke-Expression",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.loadConfig(cmd)
			if err != nil {
				return err
			}
			sel, err := cfg.Selection(nil)
			if err != nil {
				return fmt.Errorf("env: %w", err)
			}
			vars, err := envVariables(cfg, sel)
			if err != nil {
				return err
			}
			return writeEnv(cmd.OutOrStdout(), shell, vars)
		},
	}
	cmd.Flags().
		StringVar(&shell, "shell", shellSh, "the shell to print for: sh, fish or powershell")
	return cmd
}

// envVariables returns the credentials, the region, and an endpoint for each
// variable of each selected service.
//
// The endpoint variable names are the AWS_ENDPOINT_URL_<SERVICE> names in
// the AWS SDKs and Tools Reference Guide's "Identifiers for service-specific
// endpoints" table:
// https://docs.aws.amazon.com/sdkref/latest/guide/ss-endpoints-table.html
func envVariables(cfg *config.Config, sel catalog.Selection) ([]variable, error) {
	hostPorts, err := cfg.HostPorts(sel)
	if err != nil {
		return nil, fmt.Errorf("host ports: %w", err)
	}
	vars := []variable{
		{"AWS_ACCESS_KEY_ID", cfg.RootKey.ID},
		{"AWS_SECRET_ACCESS_KEY", cfg.RootKey.Secret},
		{"AWS_REGION", catalog.Region},
		// boto3 and the AWS CLI v1 read only this one.
		{"AWS_DEFAULT_REGION", catalog.Region},
	}
	for _, svc := range sel.Services() {
		port := strconv.Itoa(hostPorts.Of(svc.Name, catalog.PortAPI))
		for _, suffix := range svc.EndpointVars {
			host := cfg.PublicHost
			if suffix == "S3" {
				host = s3Host(cfg.PublicHost)
			}
			vars = append(vars, variable{
				"AWS_ENDPOINT_URL_" + suffix,
				"http://" + net.JoinHostPort(host, port),
			})
		}
	}
	return vars, nil
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
