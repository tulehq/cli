package catalog

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

var testKey = RootKey{ID: "AKIATESTTESTTEST", Secret: "secret"}

// defaultHostPorts is every port at its default.
func defaultHostPorts() HostPorts {
	out := HostPorts{}
	for _, s := range All() {
		out[s.Name] = map[string]int{}
		for _, p := range s.Ports {
			out[s.Name][p.Name] = p.Default
		}
	}
	return out
}

func mustSelect(t *testing.T, names ...string) Selection {
	t.Helper()
	sel, err := NewSelection(names)
	if err != nil {
		t.Fatalf("NewSelection(%v) error = %v", names, err)
	}
	return sel
}

func wiring(t *testing.T, names ...string) Wiring {
	t.Helper()
	return Wiring{
		Selection:  mustSelect(t, names...),
		HostPorts:  defaultHostPorts(),
		PublicHost: "localhost",
		RootKey:    testKey,
	}
}

func env(t *testing.T, w Wiring, name string) map[string]string {
	t.Helper()
	svc, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return svc.Env(w)
}

func withKey(m map[string]string) map[string]string {
	out := map[string]string{
		"TULE_Auth_AccessKeyID":     testKey.ID,
		"TULE_Auth_SecretAccessKey": testKey.Secret,
	}
	maps.Copy(out, m)
	return out
}

// envCases are the environment of a service in a selection.
var envCases = []struct {
	want      map[string]string
	name      string
	service   string
	selection []string
}{
	{
		name:      "object-store alone runs standalone on the root key",
		service:   ObjectStore,
		selection: []string{ObjectStore},
		want:      withKey(nil),
	},
	{
		name:      "object-store with identity, queues and functions",
		service:   ObjectStore,
		selection: []string{ObjectStore, Identity, Queues, Functions},
		want: withKey(map[string]string{
			"TULE_Auth_IdentityEndpoint":     "http://identity:9030",
			"TULE_Storage_QueuesEndpoint":    "http://queues:9050",
			"TULE_Storage_FunctionsEndpoint": "http://functions:9060",
		}),
	},
	{
		name:      "object-store with queues but not functions",
		service:   ObjectStore,
		selection: []string{ObjectStore, Queues},
		want:      withKey(map[string]string{"TULE_Storage_QueuesEndpoint": "http://queues:9050"}),
	},
	{
		name:      "identity alone has no accounts endpoint",
		service:   Identity,
		selection: []string{Identity},
		want:      withKey(nil),
	},
	{
		name:      "identity with accounts",
		service:   Identity,
		selection: []string{Identity, Accounts},
		want: withKey(
			map[string]string{"TULE_Identity_AccountsEndpoint": "http://accounts:9020"},
		),
	},
	{
		name:      "accounts with identity",
		service:   Accounts,
		selection: []string{Identity, Accounts},
		want:      withKey(map[string]string{"TULE_Auth_IdentityEndpoint": "http://identity:9030"}),
	},
	{
		name:      "secret-store with functions",
		service:   SecretStore,
		selection: []string{SecretStore, Functions},
		want: withKey(
			map[string]string{"TULE_Store_FunctionsEndpoint": "http://functions:9060"},
		),
	},
	{
		name:      "secret-store without functions",
		service:   SecretStore,
		selection: []string{SecretStore, Queues},
		want:      withKey(nil),
	},
	{
		name:      "mailbox with identity",
		service:   Mailbox,
		selection: []string{Mailbox, Identity},
		want:      withKey(map[string]string{"TULE_Auth_IdentityEndpoint": "http://identity:9030"}),
	},
	{
		name:      "queues always has its base URL",
		service:   Queues,
		selection: []string{Queues},
		want:      withKey(map[string]string{"TULE_Queues_BaseURL": "http://localhost:9050"}),
	},
	{
		name:      "functions without queues has no queues endpoint",
		service:   Functions,
		selection: []string{Functions},
		want:      withKey(nil),
	},
	{
		name:      "functions with queues and identity",
		service:   Functions,
		selection: []string{Functions, Queues, Identity},
		want: withKey(map[string]string{
			"TULE_Auth_IdentityEndpoint":    "http://identity:9030",
			"TULE_Functions_QueuesEndpoint": "http://queues:9050",
		}),
	},
	{
		name:      "console gets only the selected services and no key",
		service:   Console,
		selection: []string{Console, ObjectStore, Queues},
		want: map[string]string{
			"TULE_Console_ObjectStoreEndpoint": "http://object-store:9000",
			"TULE_Console_QueuesEndpoint":      "http://queues:9050",
		},
	},
}

func TestEnvPerSelection(t *testing.T) {
	for _, tt := range envCases {
		t.Run(tt.name, func(t *testing.T) {
			got := env(t, wiring(t, tt.selection...), tt.service)
			if !maps.Equal(got, tt.want) {
				t.Errorf("Env() = %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestQueuesBaseURLUsesPublicHostAndHostPort(t *testing.T) {
	w := wiring(t, Queues)
	w.PublicHost = "dev.example"
	w.HostPorts[Queues][PortAPI] = 19050
	if got, want := env(t, w, Queues)["TULE_Queues_BaseURL"], "http://dev.example:19050"; got != want {
		t.Errorf("TULE_Queues_BaseURL = %q, want %q", got, want)
	}
	w.PublicHost = "::1"
	if got, want := env(t, w, Queues)["TULE_Queues_BaseURL"], "http://[::1]:19050"; got != want {
		t.Errorf("TULE_Queues_BaseURL = %q, want %q", got, want)
	}
}

func TestFunctionsRuntimePort(t *testing.T) {
	svc, err := Lookup(Functions)
	if err != nil {
		t.Fatal(err)
	}
	w := wiring(t, Functions)
	if _, ok := svc.Env(w)["TULE_Functions_RuntimeAPIPort"]; ok {
		t.Error("TULE_Functions_RuntimeAPIPort is set at the default port")
	}
	got := svc.Publishes(w.HostPorts[Functions])
	want := []Publish{{Host: 9060, Container: 9060}, {Host: 9061, Container: 9061}}
	if !slices.Equal(got, want) {
		t.Errorf("Publishes() = %v, want %v", got, want)
	}

	w.HostPorts[Functions] = map[string]int{PortAPI: 9160, PortRuntime: 9161}
	if got := svc.Env(w)["TULE_Functions_RuntimeAPIPort"]; got != "9161" {
		t.Errorf("TULE_Functions_RuntimeAPIPort = %q, want 9161", got)
	}
	got = svc.Publishes(w.HostPorts[Functions])
	want = []Publish{{Host: 9160, Container: 9060}, {Host: 9161, Container: 9161}}
	if !slices.Equal(got, want) {
		t.Errorf("Publishes() = %v, want %v", got, want)
	}
}

func TestDefaultSelectionLeavesOutFunctions(t *testing.T) {
	got := DefaultSelection().Names()
	want := []string{Identity, Accounts, ObjectStore, SecretStore, Mailbox, Queues, Console}
	if !slices.Equal(got, want) {
		t.Errorf("DefaultSelection() = %v, want %v", got, want)
	}
}

func TestNewSelection(t *testing.T) {
	sel := mustSelect(t, Queues, ObjectStore, Queues)
	if got, want := sel.Names(), []string{ObjectStore, Queues}; !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v in catalog order without duplicates", got, want)
	}
	if got, want := sel.String(), "object-store,queues"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if _, err := NewSelection([]string{Console, ObjectStore}); err != nil {
		t.Errorf("console with object-store: error = %v", err)
	}
}

func TestNewSelectionErrors(t *testing.T) {
	if _, err := NewSelection(nil); err == nil {
		t.Error("empty selection: error = nil")
	}
	if _, err := NewSelection([]string{Console, Console}); !errors.Is(err, ErrConsoleAlone) {
		t.Errorf("console alone: error = %v, want ErrConsoleAlone", err)
	}
	_, err := NewSelection([]string{ObjectStore, "s3"})
	var unknown UnknownError
	if !errors.As(err, &unknown) || unknown.Name != "s3" {
		t.Fatalf("unknown name: error = %v, want UnknownError for s3", err)
	}
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not list %s", err, name)
		}
	}
}

func TestEveryServiceButConsoleHasDataAndKey(t *testing.T) {
	w := wiring(t, Names()...)
	for _, svc := range All() {
		if svc.Name == Console {
			if svc.Volume != "" {
				t.Errorf("console has volume %q", svc.Volume)
			}
			continue
		}
		if svc.Volume == "" {
			t.Errorf("%s has no volume", svc.Name)
		}
		if len(svc.EndpointVars) == 0 {
			t.Errorf("%s has no endpoint variable", svc.Name)
		}
		if svc.Env(w)["TULE_Auth_AccessKeyID"] != testKey.ID {
			t.Errorf("%s is not given the root key", svc.Name)
		}
	}
}
