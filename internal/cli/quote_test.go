package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// hostileValues hold everything that could end a quoted string or run a
// command when the output of `tule env` is evaluated.
var hostileValues = []string{
	"plain",
	"",
	"it's",
	"'",
	"''",
	"‘left’right‚low‛reversed",
	"‘; Remove-Item x; ’",
	`back\slash`,
	`\`,
	`\'`,
	`ends with \`,
	"$(touch pwned)",
	"${HOME}",
	"$HOME",
	"`touch pwned`",
	"; touch pwned",
	"a;b|c&d>e<f",
	`"double"`,
	"(command substitution in fish)",
	"$env:PATH",
	"#comment",
	"* ? [glob]",
	"~",
}

// controlValues hold control characters, which writeEnv refuses in every
// shell.
var controlValues = []string{
	"a\nb",
	"line\n'; touch pwned; '",
	"tab\there",
	"carriage\rreturn",
	"nul\x00",
	"escape\x1b[31m",
	"next line\u0085",
	"\x7f",
}

func TestWriteEnvRefusesControlCharacters(t *testing.T) {
	for _, shell := range []string{shellSh, shellFish, shellPowerShell} {
		for _, v := range controlValues {
			var b bytes.Buffer
			err := writeEnv(&b, shell, []variable{{"OK", "fine"}, {"V", v}})
			if err == nil || !strings.Contains(err.Error(), "V holds a control character") {
				t.Errorf("%s: %q: error = %v, want a control character error", shell, v, err)
			}
			if b.Len() != 0 {
				t.Errorf("%s: %q: printed %q before refusing", shell, v, b.String())
			}
		}
	}
}

// unquoteFish reads s as one fish single-quoted word, in which only \\
// and \' are escapes, and reports whether it is exactly one such word.
func unquoteFish(s string) (string, bool) {
	if !strings.HasPrefix(s, "'") {
		return "", false
	}
	var b strings.Builder
	r := []rune(s[1:])
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '\\' && i+1 < len(r) && (r[i+1] == '\\' || r[i+1] == '\''):
			i++
			b.WriteRune(r[i])
		case r[i] == '\'':
			return b.String(), i == len(r)-1
		default:
			b.WriteRune(r[i])
		}
	}
	return "", false
}

// unquotePowerShell reads s as one PowerShell single-quoted word, in which
// any of the five single-quote characters closes the word unless another
// follows it, and the pair stands for the second; it reports whether s is
// exactly one such word.
func unquotePowerShell(s string) (string, bool) {
	r := []rune(s)
	if len(r) == 0 || !isPowerShellQuote(r[0]) {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(r); i++ {
		if !isPowerShellQuote(r[i]) {
			b.WriteRune(r[i])
			continue
		}
		if i+1 < len(r) && isPowerShellQuote(r[i+1]) {
			i++
			b.WriteRune(r[i])
			continue
		}
		return b.String(), i == len(r)-1
	}
	return "", false
}

// unquoteSh reads the sh form: single-quoted runs joined by \' .
func unquoteSh(s string) (string, bool) {
	var b strings.Builder
	for s != "" {
		switch {
		case strings.HasPrefix(s, `\'`):
			b.WriteByte('\'')
			s = s[2:]
		case strings.HasPrefix(s, "'"):
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(s[1 : 1+end])
			s = s[2+end:]
		default:
			return "", false
		}
	}
	return b.String(), true
}

func isPowerShellQuote(r rune) bool { return strings.ContainsRune(powerShellQuotes, r) }

func TestQuotingRoundTrips(t *testing.T) {
	shells := []struct {
		quote   func(string) string
		unquote func(string) (string, bool)
		name    string
	}{
		{quoteSh, unquoteSh, "sh"},
		{quoteFish, unquoteFish, "fish"},
		{quotePowerShell, unquotePowerShell, "powershell"},
	}
	for _, sh := range shells {
		for _, v := range hostileValues {
			quoted := sh.quote(v)
			got, ok := sh.unquote(quoted)
			if !ok || got != v {
				t.Errorf(
					"%s: %q quoted as %q reads back as %q (one word: %v)",
					sh.name,
					v,
					quoted,
					got,
					ok,
				)
			}
		}
	}
}

// TestEvalInShell evaluates the output of writeEnv in each shell that is
// installed, and holds every value to reading back exactly.
func TestEvalInShell(t *testing.T) {
	shells := []struct {
		name  string
		shell string
		// print prints the variable V, then a NUL.
		print string
		args  []string
	}{
		{"sh", shellSh, `printf '%s\0' "$V"`, []string{"-c"}},
		{"bash", shellSh, `printf '%s\0' "$V"`, []string{"-c"}},
		{"fish", shellFish, `printf '%s\0' "$V"`, []string{"-c"}},
		{
			"pwsh", shellPowerShell,
			`[Console]::Out.Write($env:V + [char]0)`,
			[]string{"-NoProfile", "-NonInteractive", "-Command"},
		},
	}
	for _, sh := range shells {
		t.Run(sh.name, func(t *testing.T) {
			path, err := exec.LookPath(sh.name)
			if err != nil {
				t.Skipf("%s is not installed", sh.name)
			}
			for _, v := range hostileValues {
				var script bytes.Buffer
				if err := writeEnv(&script, sh.shell, []variable{{"V", v}}); err != nil {
					t.Fatal(err)
				}
				script.WriteString(sh.print + "\n")
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				args := append(append([]string{}, sh.args...), script.String())
				dir := t.TempDir()
				//nolint:gosec // A shell from PATH, on a script this test wrote.
				cmd := exec.CommandContext(ctx, path, args...)
				cmd.Dir = dir
				out, err := cmd.Output()
				cancel()
				if err != nil {
					t.Errorf("%q: %v", v, err)
					continue
				}
				if got := strings.TrimSuffix(string(out), "\x00"); got != v {
					t.Errorf("%q evaluates to %q", v, got)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) > 0 {
					t.Errorf("%q: evaluating created %s", v, entries[0].Name())
				}
			}
		})
	}
}
