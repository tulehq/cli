package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
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

// fakeRuntime keeps a stack in memory: Up runs a project as compose would,
// recreating its services and removing the others.
type fakeRuntime struct {
	checkErr error
	// socketGroup is what SocketGroup answers; probes counts its calls.
	socketGroup    string
	containers     []runtime.Container
	volumes        []runtime.Volume
	ups            []*render.Project
	downs          []*render.Project
	removedVolumes []string
	// calls records the other operations, such as "restart queues".
	calls       []string
	probes      int
	functions   int
	downVolumes bool
}

func (f *fakeRuntime) Check(context.Context) error { return f.checkErr }

func (f *fakeRuntime) Up(_ context.Context, p *render.Project) error {
	f.ups = append(f.ups, p)
	f.containers = stacktest.Containers(p)
	for _, v := range stacktest.Volumes(p) {
		if !slices.ContainsFunc(
			f.volumes,
			func(have runtime.Volume) bool { return have.Name == v.Name },
		) {
			f.volumes = append(f.volumes, v)
		}
	}
	return nil
}

func (f *fakeRuntime) Down(_ context.Context, p *render.Project, opts runtime.DownOptions) error {
	f.downs = append(f.downs, p)
	f.downVolumes = opts.Volumes
	f.containers = nil
	if opts.Volumes {
		f.volumes = slices.DeleteFunc(f.volumes, func(v runtime.Volume) bool {
			_, ok := p.Volumes[v.Key()]
			return ok
		})
	}
	return nil
}

// UpServices recreates services of p, as compose up --no-deps does.
func (f *fakeRuntime) UpServices(_ context.Context, p *render.Project, services []string) error {
	f.calls = append(f.calls, "up "+strings.Join(services, " "))
	for _, c := range stacktest.Containers(p) {
		if slices.Contains(services, c.Labels[render.LabelService]) {
			f.containers = append(f.containers, c)
		}
	}
	for _, v := range stacktest.Volumes(p) {
		if !slices.ContainsFunc(
			f.volumes,
			func(have runtime.Volume) bool { return have.Name == v.Name },
		) {
			f.volumes = append(f.volumes, v)
		}
	}
	return nil
}

func (f *fakeRuntime) RemoveServices(
	_ context.Context,
	_ *render.Project,
	services []string,
) error {
	f.calls = append(f.calls, "rm "+strings.Join(services, " "))
	f.containers = slices.DeleteFunc(f.containers, func(c runtime.Container) bool {
		return slices.Contains(services, c.Labels[render.LabelService])
	})
	return nil
}

func (f *fakeRuntime) Logs(_ context.Context, p *render.Project, opts runtime.LogsOptions) error {
	f.calls = append(f.calls, "logs "+strings.Join(logsFlags(opts), " "))
	services := strings.Join(slices.Sorted(maps.Keys(p.Services)), ",")
	if _, err := io.WriteString(opts.Out, "logs of "+services+"\n"); err != nil {
		return fmt.Errorf("write the logs: %w", err)
	}
	return nil
}

func logsFlags(opts runtime.LogsOptions) []string {
	out := slices.Clone(opts.Services)
	if opts.Follow {
		out = append(out, "-f")
	}
	if opts.Since != "" {
		out = append(out, "--since="+opts.Since)
	}
	return out
}

func (f *fakeRuntime) Restart(_ context.Context, _ *render.Project, services []string) error {
	f.calls = append(f.calls, strings.TrimSpace("restart "+strings.Join(services, " ")))
	return nil
}

func (f *fakeRuntime) SocketGroup(context.Context, string, string) (string, error) {
	f.probes++
	if f.socketGroup == "" {
		return "", errors.New("no socket group")
	}
	return f.socketGroup, nil
}

func (f *fakeRuntime) PS(context.Context, string) ([]runtime.Container, error) {
	return f.containers, nil
}

func (f *fakeRuntime) Volumes(context.Context, string) ([]runtime.Volume, error) {
	return f.volumes, nil
}

func (f *fakeRuntime) RemoveVolumes(_ context.Context, names []string) error {
	f.removedVolumes = append(f.removedVolumes, names...)
	f.volumes = slices.DeleteFunc(f.volumes, func(v runtime.Volume) bool {
		return slices.Contains(names, v.Name)
	})
	return nil
}

func (f *fakeRuntime) RemoveFunctionContainers(context.Context, string) (int, error) {
	n := f.functions
	f.functions = 0
	return n, nil
}

// runWith executes the root command on rt with args from dir, and returns
// its standard output. Its standard error is logged.
func runWith(t *testing.T, rt runtime.Runtime, dir string, args ...string) (string, error) {
	t.Helper()
	out, _, err := runCapture(t, rt, dir, args...)
	return out, err
}

// runCapture is runWith, returning standard error too.
func runCapture(
	t *testing.T,
	rt runtime.Runtime,
	dir string,
	args ...string,
) (stdout, stderr string, err error) {
	t.Helper()
	return runInput(t, rt, dir, nil, args...)
}

// runInput is runCapture with standard input: a terminal reading stdin when
// it is not nil, and no terminal when it is.
func runInput(
	t *testing.T,
	rt runtime.Runtime,
	dir string,
	stdin io.Reader,
	args ...string,
) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	root := newRootCommand(
		func(io.Writer) runtime.Runtime { return rt },
		func() bool { return stdin != nil },
	)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetArgs(args)
	err = root.Execute()
	if errOut.Len() > 0 {
		t.Logf("tule %s: stderr:\n%s", strings.Join(args, " "), errOut.String())
	}
	return out.String(), errOut.String(), err
}

// freePorts is a config whose ports are free on this machine, so the port
// preflight passes: every default port is moved by an offset no other test
// listens at.
func freePorts(t *testing.T) string {
	t.Helper()
	for offset := 20000; offset < 40000; offset += 100 {
		if portsFree(offset) {
			return "portOffset: " + strconv.Itoa(offset) + "\n"
		}
	}
	t.Fatal("no free range of ports")
	return ""
}

func portsFree(offset int) bool {
	for _, svc := range catalog.All() {
		for _, p := range svc.Ports {
			l, err := net.Listen(
				"tcp",
				net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Default+offset)),
			)
			if err != nil {
				return false
			}
			_ = l.Close()
		}
	}
	return true
}

func selectionOf(t *testing.T, rt *fakeRuntime) string {
	t.Helper()
	st, err := stack.FromContainers("tule", rt.containers, rt.volumes)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := st.Selection()
	if err != nil {
		t.Fatal(err)
	}
	return sel.String()
}

func TestUpNewStackStartsTheDefaultSet(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	out, err := runWith(t, rt, dir, "up")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := selectionOf(t, rt), catalog.DefaultSelection().String(); got != want {
		t.Errorf("selection = %s, want %s", got, want)
	}
	if !strings.Contains(out, "Starting: identity, accounts") {
		t.Errorf("output:\n%s", out)
	}
	path := rt.containers[0].Labels[render.LabelConfig]
	if path == "" || !strings.HasSuffix(path, config.FileName) {
		t.Errorf("config label = %q, want the absolute path of tule.yaml", path)
	}
}

func TestUpAddsToTheRunningSelection(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "object-store"); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, rt, dir, "up", "queues")
	if err != nil {
		t.Fatal(err)
	}
	if got := selectionOf(t, rt); got != "object-store,queues" {
		t.Errorf("selection = %s", got)
	}
	want := "Recreating, because their wiring changed:\n  object-store: TULE_Storage_QueuesEndpoint added\nStarting: queues\n"
	if !strings.Contains(out, want) {
		t.Errorf("output:\n%s\nwant it to hold:\n%s", out, want)
	}

	// With none named, up keeps the running selection, not the default set.
	if _, err := runWith(t, rt, dir, "up"); err != nil {
		t.Fatal(err)
	}
	if got := selectionOf(t, rt); got != "object-store,queues" {
		t.Errorf("selection after a bare up = %s", got)
	}
}

func TestUpRefusesAnotherConfigFile(t *testing.T) {
	ports := freePorts(t)
	first := writeConfig(t, ports)
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, first, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	second := writeConfig(t, ports)
	_, err := runWith(t, rt, second, "up")
	if err == nil ||
		!strings.Contains(err.Error(), first) ||
		!strings.Contains(err.Error(), second) {
		t.Errorf("up from another config = %v, want both paths named", err)
	}
	if len(rt.ups) != 1 {
		t.Errorf("ran up %d times, want 1", len(rt.ups))
	}
}

func TestUpFunctionsProbesTheSocketGroup(t *testing.T) {
	rt, dir := upFunctions(t)

	// A running functions keeps its group: no probe, nothing recreated.
	out, err := runWith(t, rt, dir, "up")
	if err != nil {
		t.Fatal(err)
	}
	if rt.probes != 1 || strings.Contains(out, "Recreating") {
		t.Errorf("a bare up probed %d times in all, output:\n%s", rt.probes, out)
	}

	// --docker-gid overrides it, and the plan says so.
	out, err = runWith(t, rt, dir, "up", "--docker-gid", "1001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "functions: Docker socket group changed") {
		t.Errorf("output:\n%s", out)
	}
	if got := rt.containers[0].GroupAdd; !slices.Equal(got, []string{"1001"}) {
		t.Errorf("functions group = %v, want 1001", got)
	}
}

// upFunctions starts object-store and queues, then adds functions, and
// checks functions was given the probed group and the stack network, and
// that only object-store, which is wired to it, was recreated.
func upFunctions(t *testing.T) (*fakeRuntime, string) {
	t.Helper()
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{socketGroup: "989"}
	if _, err := runWith(t, rt, dir, "up", "queues", "object-store"); err != nil {
		t.Fatal(err)
	}
	if rt.probes != 0 {
		t.Errorf("probed the socket group %d times without functions", rt.probes)
	}
	out, err := runWith(t, rt, dir, "up", "functions")
	if err != nil {
		t.Fatal(err)
	}
	if rt.probes != 1 {
		t.Errorf("probed the socket group %d times, want once", rt.probes)
	}
	functions := rt.ups[len(rt.ups)-1].Services[catalog.Functions]
	if !slices.Equal(functions.GroupAdd, []string{"989"}) {
		t.Errorf("functions group_add = %v, want the probed group", functions.GroupAdd)
	}
	if got := functions.Environment["TULE_Functions_ContainerNetwork"]; got != "tule_default" {
		t.Errorf("TULE_Functions_ContainerNetwork = %q, want the stack network", got)
	}
	if len(functions.Ports) != 1 {
		t.Errorf("functions publishes %v, want its API port alone", functions.Ports)
	}
	want := "Recreating, because their wiring changed:\n" +
		"  object-store: TULE_Storage_FunctionsEndpoint added\n" +
		"Starting: functions\n"
	if !strings.Contains(out, want) {
		t.Errorf("output:\n%s\nwant it to hold:\n%s", out, want)
	}
	return rt, dir
}

func TestUpFunctionsProbeFailure(t *testing.T) {
	rt := &fakeRuntime{}
	_, err := runWith(t, rt, writeConfig(t, freePorts(t)), "up", "functions")
	if err == nil || !strings.Contains(err.Error(), "pass --docker-gid") {
		t.Errorf("up functions with a failing probe = %v", err)
	}
	if len(rt.ups) != 0 {
		t.Error("ran up")
	}
	_, err = runWith(t, rt, writeConfig(t, freePorts(t)), "up", "functions", "--docker-gid", "x")
	if err == nil || !strings.Contains(err.Error(), "--docker-gid") {
		t.Errorf("up --docker-gid x = %v", err)
	}
}

func TestUpAddsToAStackLeftWithTheConsoleAlone(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues", "console"); err != nil {
		t.Fatal(err)
	}
	// What an interrupted down can leave.
	rt.containers = slices.DeleteFunc(rt.containers, func(c runtime.Container) bool {
		return c.Labels[render.LabelService] == catalog.Queues
	})
	if _, err := runWith(t, rt, dir, "up", "object-store"); err != nil {
		t.Fatalf("up object-store on a stack left with the console alone = %v", err)
	}
	if got := selectionOf(t, rt); got != "object-store,console" {
		t.Errorf("selection = %s", got)
	}
}

func TestUpRefusesADowngrade(t *testing.T) {
	ports := freePorts(t)
	dir := writeConfig(t, ports)
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	queues := mustLookup(catalog.Queues)
	rewriteConfig(t, dir, ports+"versions: {queues: 0.1.0}\n")
	out, err := runWith(t, rt, dir, "up")
	if !errors.Is(err, stack.ErrDowngrade) || !strings.Contains(err.Error(), "--allow-downgrade") {
		t.Errorf("up going down a version = %v", err)
	}
	if !strings.Contains(out, "queues "+queues.Version+" -> 0.1.0 (downgrade)") {
		t.Errorf("the version change is not listed before the refusal:\n%s", out)
	}
	if len(rt.ups) != 1 {
		t.Fatalf("ran up %d times, want once", len(rt.ups))
	}
	if _, err = runWith(t, rt, dir, "up", "--allow-downgrade"); err != nil {
		t.Fatalf("up --allow-downgrade = %v", err)
	}
	if got := rt.containers[0].Image; got != queues.ImageRef("0.1.0") {
		t.Errorf("queues runs %s after the downgrade", got)
	}
	// Going back up needs no flag.
	rewriteConfig(t, dir, ports)
	if _, err = runWith(t, rt, dir, "up"); err != nil {
		t.Errorf("up going up a version = %v", err)
	}
}

// rewriteConfig replaces the tule.yaml in dir.
func rewriteConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(dir, config.FileName),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func TestUpChecksPortsFirst(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	rt := &fakeRuntime{}
	dir := writeConfig(t, freePorts(t)+"ports: {queues: "+strconv.Itoa(port)+"}\n")
	_, err = runWith(t, rt, dir, "up", "queues")
	if err == nil || !strings.Contains(err.Error(), "ports: {queues: <port>}") {
		t.Errorf("up on a busy port = %v", err)
	}
	if len(rt.ups) != 0 {
		t.Error("ran up")
	}
}

func TestUpSkipPortCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	rt := &fakeRuntime{}
	dir := writeConfig(t, freePorts(t)+"ports: {queues: "+strconv.Itoa(port)+"}\n")
	if _, err = runWith(t, rt, dir, "up", "queues", "--skip-port-check"); err != nil {
		t.Errorf("up --skip-port-check on a busy port = %v", err)
	}
	if len(rt.ups) != 1 {
		t.Error("did not run up")
	}
}

func TestUpNamesTheMissingComposePlugin(t *testing.T) {
	rt := &fakeRuntime{checkErr: errors.New("install the Docker Compose v2 plugin")}
	_, err := runWith(t, rt, writeConfig(t, ""), "up")
	if err == nil || !strings.Contains(err.Error(), "Compose v2 plugin") {
		t.Errorf("up = %v", err)
	}
}

func TestDownServiceDropsItsWiring(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "object-store", "queues"); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, rt, dir, "down", "queues", "--volumes")
	if err != nil {
		t.Fatal(err)
	}
	if got := selectionOf(t, rt); got != "object-store" {
		t.Errorf("selection = %s", got)
	}
	objectStore := rt.containers[0]
	if _, ok := objectStore.Env["TULE_Storage_QueuesEndpoint"]; ok {
		t.Error("object-store still has queues' endpoint")
	}
	recreate := strings.Index(out, "object-store: TULE_Storage_QueuesEndpoint removed")
	remove := strings.Index(out, "Removing: queues")
	if recreate < 0 || remove < recreate {
		t.Errorf("want the recreated dependents listed before the removal:\n%s", out)
	}
	if !slices.Equal(rt.removedVolumes, []string{"tule_queuesdata"}) {
		t.Errorf("removed volumes = %v", rt.removedVolumes)
	}
}

func TestDownLastServiceRemovesTheStack(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	if _, err := runWith(t, rt, dir, "down", "queues"); err != nil {
		t.Fatal(err)
	}
	if len(rt.downs) != 1 || len(rt.containers) != 0 {
		t.Errorf("downs = %d, containers = %d", len(rt.downs), len(rt.containers))
	}
}

func TestDownRefusesConsoleAlone(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues", "console"); err != nil {
		t.Fatal(err)
	}
	_, err := runWith(t, rt, dir, "down", "queues")
	if err == nil || !strings.Contains(err.Error(), "leaves console alone") {
		t.Errorf("down = %v", err)
	}
}

func TestDownRefusesAServiceNotRunning(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	_, err := runWith(t, rt, dir, "down", "mailbox")
	if err == nil || !strings.Contains(err.Error(), "does not run mailbox") {
		t.Errorf("down = %v", err)
	}
}

func TestDownStack(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "object-store", "queues"); err != nil {
		t.Fatal(err)
	}
	// queues' volume is left from before, outside the project down takes.
	if _, err := runWith(t, rt, dir, "down", "queues"); err != nil {
		t.Fatal(err)
	}
	rt.functions = 2
	foreign := runtime.Volume{Name: "tule_pgdata", Labels: map[string]string{
		runtime.LabelProject: "tule", runtime.LabelVolume: "pgdata",
	}}
	rt.volumes = append(rt.volumes, foreign)
	// down without names works from any directory.
	out, err := runWith(t, rt, t.TempDir(), "down", "--volumes")
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.containers) != 0 || len(rt.volumes) != 1 || rt.volumes[0].Name != foreign.Name {
		t.Errorf("containers = %v, volumes = %v; want only the foreign volume left",
			rt.containers, rt.volumes)
	}
	checkDownProject(t, rt, catalog.ObjectStore)
	if !strings.Contains(out, "removed 2 function containers") ||
		!strings.Contains(out, "stack tule is down") {
		t.Errorf("output:\n%s", out)
	}
}

// checkDownProject checks compose down ran once, with --volumes, on a
// project of the services named.
func checkDownProject(t *testing.T, rt *fakeRuntime, services ...string) {
	t.Helper()
	if len(rt.downs) != 1 || !rt.downVolumes {
		t.Fatalf("compose down ran %d times, with --volumes %v; want once, with it",
			len(rt.downs), rt.downVolumes)
	}
	if got := slices.Sorted(maps.Keys(rt.downs[0].Services)); !slices.Equal(got, services) {
		t.Errorf("down project services = %v, want %v", got, services)
	}
}

func TestStatus(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "object-store", "queues"); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, rt, t.TempDir(), "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got statusJSON
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if got.Stack != "tule" || len(got.Services) != 2 || !got.Services[1].Healthy ||
		got.Services[1].Volume != "tule_queuesdata" {
		t.Errorf("status = %+v", got)
	}
}

func TestStatusUnhealthy(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "object-store", "queues"); err != nil {
		t.Fatal(err)
	}
	rt.containers[1].Health = "unhealthy"
	out, err := runWith(t, rt, t.TempDir(), "status")
	if err == nil || !strings.Contains(err.Error(), "not healthy: queues") {
		t.Errorf("status with queues unhealthy = %v", err)
	}
	if !strings.Contains(out, "queues") || !strings.Contains(out, "unhealthy") {
		t.Errorf("table:\n%s", out)
	}
}

func TestStatusWithoutAStack(t *testing.T) {
	_, err := runWith(t, &fakeRuntime{}, t.TempDir(), "status")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("status = %v", err)
	}
	out, err := runWith(t, &fakeRuntime{}, t.TempDir(), "status", "--json")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("status --json = %v", err)
	}
	var got statusJSON
	if jsonErr := json.Unmarshal([]byte(out), &got); jsonErr != nil ||
		got.Services == nil || len(got.Services) != 0 || got.Configs == nil {
		t.Errorf("status --json = %q (%v), want empty lists", out, jsonErr)
	}
}

// twoConfigStack is a stack whose containers were started from two config
// files: queues from one, object-store from none.
func twoConfigStack(t *testing.T) (rt *fakeRuntime, dir string) {
	t.Helper()
	ports := freePorts(t)
	dir = writeConfig(t, ports)
	rt = &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Parse([]byte(ports))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := catalog.NewSelection([]string{catalog.ObjectStore})
	if err != nil {
		t.Fatal(err)
	}
	p, err := render.Render(render.Input{Stack: "tule", Config: cfg, Selection: sel})
	if err != nil {
		t.Fatal(err)
	}
	rt.containers = append(rt.containers, stacktest.Containers(p)...)
	rt.volumes = append(rt.volumes, stacktest.Volumes(p)...)
	return rt, dir
}

func TestTwoConfigsStatusAndDownStillWork(t *testing.T) {
	rt, dir := twoConfigStack(t)
	_, err := runWith(t, rt, dir, "up")
	if err == nil || !strings.Contains(err.Error(), "tule down --stack tule") {
		t.Errorf("up = %v, want it to point at tule down", err)
	}
	_, stderr, err := runCapture(t, rt, t.TempDir(), "status")
	if err != nil || !strings.Contains(stderr, "more than one config file") {
		t.Errorf("status = %v, stderr %q; want a warning and success", err, stderr)
	}
	_, stderr, err = runCapture(t, rt, t.TempDir(), "down", "--volumes")
	if err != nil || !strings.Contains(stderr, "more than one config file") {
		t.Errorf("down --volumes = %v, stderr %q", err, stderr)
	}
	if len(rt.containers) != 0 || len(rt.volumes) != 0 {
		t.Errorf("containers = %v, volumes = %v", rt.containers, rt.volumes)
	}
}

func TestDownServiceKeepingFunctions(t *testing.T) {
	dir := writeConfig(t, freePorts(t))
	rt := &fakeRuntime{socketGroup: "989"}
	if _, err := runWith(t, rt, dir, "up", "functions", "queues", "object-store"); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, rt, dir, "down", "object-store")
	if err != nil {
		t.Fatalf("down object-store = %v", err)
	}
	if rt.probes != 1 {
		t.Errorf("probed %d times; down should keep the running functions' group", rt.probes)
	}
	if strings.Contains(out, "functions:") {
		t.Errorf("down object-store recreates functions:\n%s", out)
	}
	if got := selectionOf(t, rt); got != "queues,functions" {
		t.Errorf("selection = %s", got)
	}
	rt.functions = 3
	out, err = runWith(t, rt, dir, "down", "functions")
	if err != nil {
		t.Fatal(err)
	}
	if got := selectionOf(t, rt); got != "queues" {
		t.Errorf("selection = %s", got)
	}
	if !strings.Contains(out, "removed 3 function containers") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDownServicesSkipPortCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ports := freePorts(t)
	dir := writeConfig(t, ports)
	rt := &fakeRuntime{}
	if _, err = runWith(t, rt, dir, "up", "object-store", "queues"); err != nil {
		t.Fatal(err)
	}
	// object-store is recreated on a port something else holds.
	busy := "ports: {object-store: " + strconv.Itoa(l.Addr().(*net.TCPAddr).Port) + "}\n"
	if err = os.WriteFile(
		filepath.Join(dir, config.FileName),
		[]byte(ports+busy),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = runWith(t, rt, dir, "down", "queues"); err == nil ||
		!strings.Contains(err.Error(), "ports: {object-store: <port>}") {
		t.Errorf("down queues onto a busy port = %v", err)
	}
	if _, err = runWith(t, rt, dir, "down", "queues", "--skip-port-check"); err != nil {
		t.Errorf("down queues --skip-port-check = %v", err)
	}
	if got := selectionOf(t, rt); got != "object-store" {
		t.Errorf("selection = %s", got)
	}
}

func TestDownStackWithTwoConfigsSaysItRemovesIt(t *testing.T) {
	rt, _ := twoConfigStack(t)
	_, stderr, err := runCapture(t, rt, t.TempDir(), "down")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "remove it with tule down") ||
		!strings.Contains(stderr, "more than one config file") ||
		!strings.Contains(stderr, "removing all of it") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestBareUpWarnsConfiguredServicesAreIgnored(t *testing.T) {
	dir := writeConfig(t, freePorts(t)+"services: [object-store]\n")
	rt := &fakeRuntime{}
	if _, err := runWith(t, rt, dir, "up"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runCapture(t, rt, dir, "up")
	if err != nil || strings.Contains(stderr, "services:") {
		t.Errorf("up matching services: = %v, stderr %q; want no warning", err, stderr)
	}
	if _, err = runWith(t, rt, dir, "up", "queues"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runCapture(t, rt, dir, "up")
	if err != nil ||
		!strings.Contains(
			stderr,
			"services: (object-store) differ from the running stack's (object-store,queues)",
		) {
		t.Errorf("up = %v, stderr %q", err, stderr)
	}
	if got := selectionOf(t, rt); got != "object-store,queues" {
		t.Errorf("selection = %s", got)
	}
}
