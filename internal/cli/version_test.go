package cli

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"

	"tule.dev/cli/internal/catalog"
)

func TestVersionString(t *testing.T) {
	tests := []struct {
		name string
		want string
		info debug.BuildInfo
	}{
		{
			name: "release from the module proxy has no VCS settings",
			info: debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}},
			want: "tule v0.1.0",
		},
		{
			name: "devel build with a revision",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "0123abc"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: "tule (devel) (0123abc)",
		},
		{
			name: "devel build from a modified tree",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "0123abc"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			want: "tule (devel) (0123abc, modified)",
		},
		{
			name: "pseudo-version with a revision",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "v0.0.0-20261115120000-0123abc45678"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "0123abc45678"},
				},
			},
			want: "tule v0.0.0-20261115120000-0123abc45678 (0123abc45678)",
		},
		{
			name: "empty version reads as devel",
			info: debug.BuildInfo{},
			want: "tule (devel)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionString(&tt.info); got != tt.want {
				t.Errorf("versionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteVersion(t *testing.T) {
	services := []*catalog.Service{
		{Name: "object-store", Image: "ghcr.io/tulehq/object-store", Version: "0.4.2"},
		{Name: "queues", Image: "ghcr.io/tulehq/queues", Version: "0.3.0"},
	}
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	var out bytes.Buffer

	if err := writeVersion(&out, info, services); err != nil {
		t.Fatalf("writeVersion() error = %v", err)
	}
	want := "tule v0.1.0\n" +
		"\n" +
		"SERVICE       IMAGE\n" +
		"object-store  ghcr.io/tulehq/object-store:0.4.2\n" +
		"queues        ghcr.io/tulehq/queues:0.3.0\n"
	if got := out.String(); got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
}

func TestVersionCommandPrints(t *testing.T) {
	cmd := newVersionCommand(func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "tule v0.1.0\n\nSERVICE") {
		t.Errorf("output = %q, want the version line, a blank line and the service table", got)
	}
	for _, svc := range catalog.All() {
		if line := svc.Name + " "; !strings.Contains(got, "\n"+line) {
			t.Errorf("output = %q, want a line for %s", got, svc.Name)
		}
		if ref := svc.ImageRef(svc.Version) + "\n"; !strings.Contains(got, ref) {
			t.Errorf("output = %q, want %q", got, ref)
		}
	}
}

func TestVersionCommandWithoutBuildInfo(t *testing.T) {
	cmd := newVersionCommand(func() (*debug.BuildInfo, bool) { return nil, false })
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error when build information is missing")
	}
}

func TestRootCommandHasVersion(t *testing.T) {
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// A test binary's main module is reported as "(devel)" or a
	// pseudo-version; either way the line starts with the program name.
	if !bytes.HasPrefix(out.Bytes(), []byte("tule ")) {
		t.Errorf("output = %q, want a line starting with %q", out.String(), "tule ")
	}
}
