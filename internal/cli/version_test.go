package cli

import (
	"bytes"
	"runtime/debug"
	"testing"
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
	if got, want := out.String(), "tule v0.1.0\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
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
