package stack_test

import (
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/config"
	"tule.dev/cli/internal/render"
	"tule.dev/cli/internal/runtime"
	"tule.dev/cli/internal/stack"
	"tule.dev/cli/internal/stack/stacktest"
)

func parse(t *testing.T, doc string) *config.Config {
	t.Helper()
	cfg, _, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func selection(t *testing.T, names ...string) catalog.Selection {
	t.Helper()
	sel, err := catalog.NewSelection(names)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func project(t *testing.T, cfg *config.Config, names ...string) *render.Project {
	t.Helper()
	p, err := render.Render(
		render.Input{Stack: "tule", Config: cfg, Selection: selection(t, names...)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func state(t *testing.T, p *render.Project) *stack.State {
	t.Helper()
	st, err := stack.FromContainers(p.Name, stacktest.Containers(p), stacktest.Volumes(p))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReadBack(t *testing.T) {
	cfg := parse(t, "")
	cfg.Path = "/work/tule.yaml"
	p := project(t, cfg, catalog.ObjectStore, catalog.Queues)
	foreign := runtime.Volume{Name: "tule_pgdata", Labels: map[string]string{
		runtime.LabelProject: "tule", runtime.LabelVolume: "pgdata",
	}}
	st, err := stack.FromContainers(
		"tule",
		stacktest.Containers(p),
		append(stacktest.Volumes(p), foreign),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running() {
		t.Fatal("Running() = false")
	}
	if got := st.Configs(); !reflect.DeepEqual(got, []string{"/work/tule.yaml"}) {
		t.Errorf("Configs() = %v", got)
	}
	sel, err := st.Selection()
	if err != nil {
		t.Fatal(err)
	}
	if got := sel.String(); got != "object-store,queues" {
		t.Errorf("Selection() = %s", got)
	}
	if got := st.VolumeNames(
		[]string{"queuesdata"},
	); !reflect.DeepEqual(
		got,
		[]string{"tule_queuesdata"},
	) {
		t.Errorf("VolumeNames() = %v", got)
	}
	got := st.VolumeNames(stack.CatalogVolumeKeys())
	if want := []string{"tule_objectdata", "tule_queuesdata"}; !reflect.DeepEqual(got, want) {
		t.Errorf("VolumeNames(catalog) = %v, want %v and not the foreign tule_pgdata", got, want)
	}
}

func TestReadBackEmpty(t *testing.T) {
	st, err := stack.FromContainers("tule", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running() {
		t.Error("Running() = true for a stack without containers")
	}
	if err := st.CheckConfig("/anywhere/tule.yaml"); err != nil {
		t.Errorf("CheckConfig() on a new stack = %v", err)
	}
	if _, err := st.Selection(); err == nil {
		t.Error("Selection() of a stack without containers did not fail")
	}
}

func TestReadBackConsoleAlone(t *testing.T) {
	// What an interrupted down can leave.
	containers := stacktest.Containers(project(t, parse(t, ""), catalog.Queues, catalog.Console))
	st, err := stack.FromContainers("tule", containers[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Selection()
	if !errors.Is(err, catalog.ErrConsoleAlone) {
		t.Errorf("Selection() = %v, want %v", err, catalog.ErrConsoleAlone)
	}
	if got := st.Status(); len(got) != 1 || got[0].Name != catalog.Console {
		t.Errorf("Status() = %+v", got)
	}
}

func TestReadBackRefusesAForeignContainer(t *testing.T) {
	containers := stacktest.Containers(project(t, parse(t, ""), catalog.Queues))
	containers = append(
		containers,
		runtime.Container{Name: "tule-db-1", Labels: map[string]string{}},
	)
	_, err := stack.FromContainers("tule", containers, nil)
	if err == nil || !strings.Contains(err.Error(), "tule-db-1, which tule did not create") {
		t.Errorf("FromContainers() = %v", err)
	}
}

func TestReadBackTwoConfigs(t *testing.T) {
	a := parse(t, "")
	a.Path = "/a/tule.yaml"
	b := parse(t, "")
	containers := append(
		stacktest.Containers(project(t, a, catalog.Queues)),
		stacktest.Containers(project(t, b, catalog.ObjectStore))...)
	st, err := stack.FromContainers("tule", containers, nil)
	if err != nil {
		t.Fatalf("FromContainers() = %v, want the state, so the stack can be removed", err)
	}
	if got := st.Configs(); !reflect.DeepEqual(got, []string{"", "/a/tule.yaml"}) {
		t.Errorf("Configs() = %v", got)
	}
	conflict := st.ConfigConflict()
	if conflict == nil ||
		!strings.Contains(conflict.Error(), "queues from /a/tule.yaml") ||
		!strings.Contains(conflict.Error(), "object-store from no tule.yaml") ||
		!strings.Contains(conflict.Error(), "tule down --stack tule") {
		t.Errorf("ConfigConflict() = %v", conflict)
	}
	for _, path := range []string{"", "/a/tule.yaml"} {
		if err := st.CheckConfig(path); err == nil || err.Error() != conflict.Error() {
			t.Errorf("CheckConfig(%q) = %v, want the conflict", path, err)
		}
	}
}

func TestCheckConfig(t *testing.T) {
	cfg := parse(t, "")
	cfg.Path = "/a/tule.yaml"
	st := state(t, project(t, cfg, catalog.Queues))
	if err := st.ConfigConflict(); err != nil {
		t.Errorf("ConfigConflict() = %v", err)
	}
	if err := st.CheckConfig("/a/tule.yaml"); err != nil {
		t.Errorf("CheckConfig(same) = %v", err)
	}
	err := st.CheckConfig("/b/tule.yaml")
	if err == nil ||
		!strings.Contains(err.Error(), "started from /a/tule.yaml") ||
		!strings.Contains(err.Error(), "reads /b/tule.yaml") {
		t.Errorf("CheckConfig(other) = %v", err)
	}
	err = st.CheckConfig("")
	if err == nil || !strings.Contains(err.Error(), "reads no tule.yaml") {
		t.Errorf("CheckConfig(none) = %v", err)
	}
}

func bindings(t *testing.T, cfg *config.Config, names ...string) []stack.Binding {
	t.Helper()
	b, err := stack.Bindings(cfg, selection(t, names...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanAddingQueuesRewiresObjectStore(t *testing.T) {
	cfg := parse(t, "")
	st := state(t, project(t, cfg, catalog.ObjectStore))
	next := project(t, cfg, catalog.ObjectStore, catalog.Queues)
	plan := stack.NewPlan(st, next, bindings(t, cfg, catalog.ObjectStore, catalog.Queues))
	want := stack.Plan{
		Recreate: []stack.Change{{
			Service: catalog.ObjectStore,
			Reasons: []string{"TULE_Storage_QueuesEndpoint added"},
		}},
		Add: []string{catalog.Queues},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}
}

func TestPlanRemovingQueuesDropsItsWiring(t *testing.T) {
	cfg := parse(t, "")
	st := state(t, project(t, cfg, catalog.ObjectStore, catalog.Queues, catalog.Console))
	next := project(t, cfg, catalog.ObjectStore, catalog.Console)
	plan := stack.NewPlan(st, next, bindings(t, cfg, catalog.ObjectStore, catalog.Console))
	want := stack.Plan{
		Recreate: []stack.Change{
			{
				Service: catalog.ObjectStore,
				Reasons: []string{"TULE_Storage_QueuesEndpoint removed"},
			},
			{Service: catalog.Console, Reasons: []string{"TULE_Console_QueuesEndpoint removed"}},
		},
		Remove: []string{catalog.Queues},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}
	var b strings.Builder
	if err := plan.Write(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Index(out, "object-store: TULE_Storage_QueuesEndpoint removed") >
		strings.Index(out, "Removing: queues") {
		t.Errorf("the recreated services are not listed first:\n%s", out)
	}
}

func TestPlanNoChange(t *testing.T) {
	cfg := parse(t, "")
	p := project(t, cfg, catalog.ObjectStore, catalog.Queues)
	plan := stack.NewPlan(state(t, p), p, bindings(t, cfg, catalog.ObjectStore, catalog.Queues))
	if !reflect.DeepEqual(plan, stack.Plan{}) {
		t.Errorf("plan = %+v, want nothing", plan)
	}
	var b strings.Builder
	if err := plan.Write(&b); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Errorf("Write() = %q, want nothing", b.String())
	}
}

func TestPlanImageEnvIsNotWiring(t *testing.T) {
	cfg := parse(t, "")
	p := project(t, cfg, catalog.Queues)
	containers := stacktest.Containers(p)
	// A variable the image sets, and one tule set and no longer does.
	containers[0].ImageEnv["TULE_Server_Addr"] = ":9050"
	containers[0].Env["TULE_Server_Addr"] = ":9050"
	containers[0].Env["TULE_Old"] = "x"
	st, err := stack.FromContainers("tule", containers, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := stack.NewPlan(st, p, bindings(t, cfg, catalog.Queues))
	want := []stack.Change{{Service: catalog.Queues, Reasons: []string{"TULE_Old removed"}}}
	if !reflect.DeepEqual(plan.Recreate, want) {
		t.Errorf("Recreate = %+v, want %+v", plan.Recreate, want)
	}
}

func TestPlanVersionsAndPorts(t *testing.T) {
	old := parse(t, "versions: {queues: 0.1.0}")
	st := state(t, project(t, old, catalog.Queues, catalog.ObjectStore))
	cfg := parse(t, "ports: {object-store: 9100}")
	next := project(t, cfg, catalog.Queues, catalog.ObjectStore)
	plan := stack.NewPlan(st, next, bindings(t, cfg, catalog.Queues, catalog.ObjectStore))
	queues := stacktest.Lookup(catalog.Queues)
	wantVersions := []stack.VersionChange{
		{Service: catalog.Queues, From: "0.1.0", To: queues.Version},
	}
	if !reflect.DeepEqual(plan.Versions, wantVersions) {
		t.Errorf("Versions = %+v, want %+v", plan.Versions, wantVersions)
	}
	wantRecreate := []stack.Change{
		{Service: catalog.ObjectStore, Reasons: []string{"host ports changed"}},
	}
	if !reflect.DeepEqual(plan.Recreate, wantRecreate) {
		t.Errorf("Recreate = %+v, want %+v", plan.Recreate, wantRecreate)
	}
}

// listen holds a free port on 127.0.0.1 until the test ends.
func listen(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

func never(string, int) bool { return false }

func TestPreflightFree(t *testing.T) {
	free := []stack.Binding{
		{Service: "queues", Port: catalog.PortAPI, HostIP: "127.0.0.1", HostPort: 0},
	}
	if err := stack.Preflight(t.Context(), free, never); err != nil {
		t.Errorf("Preflight(free) = %v", err)
	}
}

func TestPreflightBusyAPIPort(t *testing.T) {
	busy := listen(t)
	api := stack.Binding{
		Service:  "queues",
		Port:     catalog.PortAPI,
		HostIP:   "127.0.0.1",
		HostPort: busy,
	}
	err := stack.Preflight(t.Context(), []stack.Binding{api}, never)
	if err == nil ||
		!strings.Contains(err.Error(), "queues's api port 127.0.0.1:"+strconv.Itoa(busy)) ||
		!strings.Contains(err.Error(), "ports: {queues: <port>}") ||
		!strings.Contains(err.Error(), "portOffset:") {
		t.Errorf("Preflight(busy api) = %v", err)
	}
	held := func(ip string, port int) bool { return ip == "127.0.0.1" && port == busy }
	if err := stack.Preflight(t.Context(), []stack.Binding{api}, held); err != nil {
		t.Errorf("Preflight(held by the stack) = %v", err)
	}
}

func TestPreflightBusyRuntimePort(t *testing.T) {
	busy := listen(t)
	runtimePort := stack.Binding{
		Service: "functions", Port: catalog.PortRuntime, HostIP: "127.0.0.1", HostPort: busy,
	}
	err := stack.Preflight(t.Context(), []stack.Binding{runtimePort}, never)
	if err == nil || strings.Contains(err.Error(), "ports: {") ||
		!strings.Contains(err.Error(), "portOffset:") {
		t.Errorf("Preflight(busy runtime) = %v, want portOffset named alone", err)
	}
}

func TestHeld(t *testing.T) {
	cfg := parse(t, "")
	st := state(t, project(t, cfg, catalog.Queues))
	if !st.Held("127.0.0.1", 9050) {
		t.Error("Held(queues' port) = false")
	}
	if st.Held("127.0.0.1", 9000) || st.Held("0.0.0.0", 9050) {
		t.Error("Held() is true for a port the stack does not publish")
	}
}

func TestBindingsFollowPortOffset(t *testing.T) {
	cfg := parse(t, "portOffset: 100\nports: {queues: 7000}")
	got := bindings(t, cfg, catalog.ObjectStore, catalog.Queues)
	want := []stack.Binding{
		{
			Service:   catalog.ObjectStore,
			Port:      catalog.PortAPI,
			HostIP:    "127.0.0.1",
			HostPort:  9100,
			Container: 9000,
		},
		{
			Service:   catalog.Queues,
			Port:      catalog.PortAPI,
			HostIP:    "127.0.0.1",
			HostPort:  7000,
			Container: 9050,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Bindings() = %+v, want %+v", got, want)
	}
}

func TestStatus(t *testing.T) {
	cfg := parse(t, "")
	containers := stacktest.Containers(project(t, cfg, catalog.Queues, catalog.Console))
	// In name order: console, queues. console has no health check, and
	// queues is unhealthy.
	containers[0].Health = ""
	containers[1].Health = "unhealthy"
	st, err := stack.FromContainers("tule", containers, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := st.Status()
	console := stacktest.Lookup(catalog.Console)
	queues := stacktest.Lookup(catalog.Queues)
	want := []stack.ServiceStatus{
		{
			Name: catalog.Queues, State: "running", Health: "unhealthy",
			Version: queues.Version, Address: "127.0.0.1:9050", Volume: "tule_queuesdata",
		},
		{
			Name: catalog.Console, State: "running", Healthy: true,
			Version: console.Version, Address: "127.0.0.1:9070",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status() =\n%+v\nwant\n%+v", got, want)
	}
}
