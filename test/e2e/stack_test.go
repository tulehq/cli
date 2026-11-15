//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"tule.dev/cli/internal/catalog"
	"tule.dev/cli/internal/stack"
)

const offset = 100

// TestStacks runs one stack of the default set and a second one beside it
// with portOffset, and takes them through up, status, down of one service,
// and down --volumes. The steps build on each other.
func TestStacks(t *testing.T) {
	a := newStack(t, "e2ea", "")
	// compose must not read a .env where tule runs: this one would keep
	// down queues from removing queues, and fail to parse.
	if err := os.WriteFile(filepath.Join(a.dir, ".env"),
		[]byte("COMPOSE_IGNORE_ORPHANS=true\nthis line is not a variable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newStack(t, "e2eb", "portOffset: "+strconv.Itoa(offset)+"\n")

	out := a.must("up")
	if !strings.Contains(out, "stack e2ea is up: "+catalog.DefaultSelection().String()) {
		t.Fatalf("up output does not end with the selection:\n%s", out)
	}
	checkStatus(t, a, catalog.DefaultSelection().Names(), 0)
	checkLabels(t, a)

	exerciseAll(t, a.awsConfig())

	b.must("up", catalog.ObjectStore, catalog.Queues)
	checkStatus(t, b, []string{catalog.ObjectStore, catalog.Queues}, offset)
	checkSecondStack(t, b.awsConfig())

	checkRefusals(t, a, b)
	checkDownQueues(t, a)
	checkEnvReadBack(t, a)
	checkLogs(t, a)
	checkRestartKeepsData(t, a)
	checkReset(t, a)
	checkUpFunctions(t, a)

	for _, s := range []*stackDir{b, a} {
		out := s.must("down", "--volumes")
		if !strings.Contains(out, "stack "+s.name+" is down") {
			t.Errorf("down output:\n%s", out)
		}
		if containers, volumes := stackResources(t, s.name); containers != "" || volumes != "" {
			t.Errorf("stack %s left containers %q and volumes %q", s.name, containers, volumes)
		}
	}
	if left := probeContainers(t); len(left) > 0 {
		t.Errorf("down left function containers: %v", left)
	}
}

// checkStatus checks every service named is running, healthy, at the
// catalog version, on its default host port plus offset, with its volume.
func checkStatus(t *testing.T, s *stackDir, names []string, offset int) {
	t.Helper()
	status := s.status()
	if len(status) != len(names) {
		t.Errorf("status lists %d services, want %d: %v", len(status), len(names), status)
	}
	for _, name := range names {
		svc, err := catalog.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := status[name]
		if !ok {
			t.Errorf("status has no %s", name)
			continue
		}
		want := stack.ServiceStatus{
			Name:    name,
			State:   stack.StateRunning,
			Health:  got.Health, // Empty for an image without a health check.
			Version: svc.Version,
			Address: "127.0.0.1:" + strconv.Itoa(svc.Ports[0].Default+offset),
			Healthy: true,
		}
		if svc.Volume != "" {
			want.Volume = s.name + "_" + svc.Volume
		}
		if got != want {
			t.Errorf("status of %s = %+v, want %+v", name, got, want)
		}
	}
}

// checkLabels checks the config path tule reads back is on the containers,
// and that neither containers nor volumes carry a label that would change
// with the selection or a version: compose recreates a container, and asks
// to recreate a volume, when its labels change.
func checkLabels(t *testing.T, s *stackDir) {
	t.Helper()
	container := s.name + "-" + catalog.Queues + "-1"
	if got := docker(t, "container", "inspect", "--format",
		`{{index .Config.Labels "dev.tule.config"}}`, container); got != s.configPath() {
		t.Errorf("dev.tule.config = %q, want %q", got, s.configPath())
	}
	if got := docker(t, "container", "inspect", "--format",
		`{{json .Config.Labels}}`, container); strings.Contains(got, "dev.tule.selection") {
		t.Errorf("container labels = %s, want no dev.tule.selection", got)
	}
	objectStore, err := catalog.Lookup(catalog.ObjectStore)
	if err != nil {
		t.Fatal(err)
	}
	volume := s.name + "_" + objectStore.Volume
	if got := docker(
		t,
		"volume",
		"inspect",
		"--format",
		`{{json .Labels}}`,
		volume,
	); strings.Contains(
		got,
		"dev.tule.",
	) {
		t.Errorf("volume %s labels = %s, want no tule labels", volume, got)
	}
}

// containerIDs maps each service of the stack to its container ID.
func containerIDs(t *testing.T, name string) map[string]string {
	t.Helper()
	out := docker(t, "ps", "--all", "--no-trunc",
		"--filter", "label=com.docker.compose.project="+name,
		"--format", `{{.Label "dev.tule.service"}} {{.ID}}`)
	ids := map[string]string{}
	for line := range strings.Lines(out) {
		svc, id, _ := strings.Cut(strings.TrimSpace(line), " ")
		ids[svc] = id
	}
	return ids
}

// recreated returns the services in both maps whose container changed, in
// catalog order.
func recreated(before, after map[string]string) []string {
	var out []string
	for _, name := range catalog.Names() {
		b, inBefore := before[name]
		a, inAfter := after[name]
		if inBefore && inAfter && a != b {
			out = append(out, name)
		}
	}
	return out
}

// exerciseAll makes a cheap call or two against each service the default
// set starts.
func exerciseAll(t *testing.T, cfg aws.Config) {
	t.Helper()
	exerciseStorage(t, cfg)
	exerciseIdentity(t, cfg)
	exerciseMailAndAccounts(t, cfg)
}

// exerciseStorage calls object-store, queues and secret-store.
func exerciseStorage(t *testing.T, cfg aws.Config) {
	t.Helper()
	ctx := t.Context()
	s3c := s3.NewFromConfig(cfg)
	if _, err := s3c.CreateBucket(
		ctx,
		&s3.CreateBucketInput{Bucket: aws.String("e2e-a")},
	); err != nil {
		t.Fatalf("S3 CreateBucket: %v", err)
	}
	if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("e2e-a"), Key: aws.String("hello.txt"), Body: strings.NewReader("hello"),
	}); err != nil {
		t.Errorf("S3 PutObject: %v", err)
	}

	sqsc := sqs.NewFromConfig(cfg)
	queue, err := sqsc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("e2e")})
	if err != nil {
		t.Fatalf("SQS CreateQueue: %v", err)
	}
	if _, err = sqsc.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: queue.QueueUrl, MessageBody: aws.String("hello"),
	}); err != nil {
		t.Errorf("SQS SendMessage: %v", err)
	}

	sm := secretsmanager.NewFromConfig(cfg)
	if _, err = sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String("e2e"), SecretString: aws.String("s3cret"),
	}); err != nil {
		t.Errorf("Secrets Manager CreateSecret: %v", err)
	}
}

// exerciseIdentity calls identity's STS and IAM.
func exerciseIdentity(t *testing.T, cfg aws.Config) {
	t.Helper()
	ctx := t.Context()

	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Errorf("STS GetCallerIdentity: %v", err)
	} else {
		t.Logf("caller: %s", aws.ToString(who.Arn))
	}
	if _, err := iam.NewFromConfig(cfg).CreateUser(ctx, &iam.CreateUserInput{
		UserName: aws.String("e2e"),
	}); err != nil {
		t.Errorf("IAM CreateUser: %v", err)
	}
}

// exerciseMailAndAccounts calls mailbox's SES v2 and accounts'
// Organizations.
func exerciseMailAndAccounts(t *testing.T, cfg aws.Config) {
	t.Helper()
	ctx := t.Context()

	ses := sesv2.NewFromConfig(cfg)
	if _, err := ses.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String("e2e@example.com"),
	}); err != nil {
		t.Errorf("SES CreateEmailIdentity: %v", err)
	}

	org := organizations.NewFromConfig(cfg)
	if _, err := org.CreateOrganization(ctx, &organizations.CreateOrganizationInput{}); err != nil {
		t.Errorf("Organizations CreateOrganization: %v", err)
	}
	if _, err := org.DescribeOrganization(
		ctx,
		&organizations.DescribeOrganizationInput{},
	); err != nil {
		t.Errorf("Organizations DescribeOrganization: %v", err)
	}
}

// checkSecondStack checks the second stack serves its own data on its own
// ports: the first stack's bucket is not there.
func checkSecondStack(t *testing.T, cfg aws.Config) {
	t.Helper()
	ctx := t.Context()
	s3c := s3.NewFromConfig(cfg)
	if _, err := s3c.CreateBucket(
		ctx,
		&s3.CreateBucketInput{Bucket: aws.String("e2e-b")},
	); err != nil {
		t.Fatalf("S3 CreateBucket on the second stack: %v", err)
	}
	list, err := s3c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("S3 ListBuckets on the second stack: %v", err)
	}
	names := make([]string, 0, len(list.Buckets))
	for _, bucket := range list.Buckets {
		names = append(names, aws.ToString(bucket.Name))
	}
	if !slices.Equal(names, []string{"e2e-b"}) {
		t.Errorf("the second stack's buckets = %v, want only its own", names)
	}
	queue, err := sqs.NewFromConfig(cfg).
		CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("e2e")})
	if err != nil {
		t.Fatalf("SQS CreateQueue on the second stack: %v", err)
	}
	wantPort := ":" + strconv.Itoa(9050+offset) + "/"
	if !strings.Contains(aws.ToString(queue.QueueUrl), wantPort) {
		t.Errorf("queue URL %s is not on the second stack's port", aws.ToString(queue.QueueUrl))
	}
}

// checkRefusals checks up refuses a stack started from another config
// file, and a stack whose ports are taken.
func checkRefusals(t *testing.T, a, b *stackDir) {
	t.Helper()
	out, err := a.tuleIn(b.dir, "up")
	if err == nil || !strings.Contains(out, a.configPath()) ||
		!strings.Contains(out, b.configPath()) {
		t.Errorf("up of stack %s from another config: %v\n%s", a.name, err, out)
	}

	c := newStack(t, "e2ec", "")
	out, err = c.tule("up", catalog.ObjectStore)
	if err == nil || !strings.Contains(out, "ports: {object-store: <port>}") {
		t.Errorf("up on ports another stack holds: %v\n%s", err, out)
	}
	if containers, _ := stackResources(t, c.name); containers != "" {
		t.Errorf("the refused up created containers: %s", containers)
	}
}

// checkDownQueues removes queues and checks its wiring is dropped and its
// data kept, then adds it back.
func checkDownQueues(t *testing.T, a *stackDir) {
	t.Helper()
	objectStore := a.name + "-" + catalog.ObjectStore + "-1"
	if _, ok := containerEnv(t, objectStore)["TULE_Storage_QueuesEndpoint"]; !ok {
		t.Fatal("object-store has no queues endpoint before down queues")
	}
	before := containerIDs(t, a.name)
	out := a.must("down", catalog.Queues)
	after := containerIDs(t, a.name)
	checkRecreated(t, "down queues", out, before, after)
	if _, ok := after[catalog.Queues]; ok {
		t.Error("queues' container is still there after down queues")
	}
	recreate := strings.Index(out, "object-store: TULE_Storage_QueuesEndpoint removed")
	remove := strings.Index(out, "Removing: queues")
	if recreate < 0 || remove < recreate {
		t.Errorf("down queues does not list object-store's recreate before the removal:\n%s", out)
	}
	if _, ok := containerEnv(t, objectStore)["TULE_Storage_QueuesEndpoint"]; ok {
		t.Error("object-store still has queues' endpoint after down queues")
	}
	if _, ok := containerEnv(t, a.name+"-"+catalog.Console+"-1")["TULE_Console_QueuesEndpoint"]; ok {
		t.Error("console still has queues' endpoint after down queues")
	}
	rest := slices.DeleteFunc(catalog.DefaultSelection().Names(), func(n string) bool {
		return n == catalog.Queues
	})
	checkStatus(t, a, rest, 0)
	queuesVolume := a.name + "_queuesdata"
	if got := docker(
		t,
		"volume",
		"ls",
		"--quiet",
		"--filter",
		"name="+queuesVolume,
	); got != queuesVolume {
		t.Errorf("queues' volume is gone after down without --volumes: %q", got)
	}

	before = containerIDs(t, a.name)
	out = a.must("up", catalog.Queues)
	checkRecreated(t, "up queues", out, before, containerIDs(t, a.name))
	if !strings.Contains(out, "object-store: TULE_Storage_QueuesEndpoint added") {
		t.Errorf("up queues does not list object-store's recreate:\n%s", out)
	}
	// The queue made before is still there, from the kept volume.
	if _, err := sqs.NewFromConfig(a.awsConfig()).GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{
		QueueName: aws.String("e2e"),
	}); err != nil {
		t.Errorf("SQS GetQueueUrl after queues came back: %v", err)
	}
	checkStatus(t, a, catalog.DefaultSelection().Names(), 0)
}

// wantRecreated is what adding or removing queues recreates in the default
// set: the services wired to it.
var wantRecreated = []string{catalog.ObjectStore, catalog.Console}

// checkRecreated checks the services whose containers changed are exactly
// the ones the plan in out listed as recreated, and those wired to queues.
func checkRecreated(t *testing.T, step, out string, before, after map[string]string) {
	t.Helper()
	checkRecreatedAre(t, step, out, before, after, wantRecreated)
}

// checkRecreatedAre checks the services whose containers changed are
// exactly the ones the plan in out listed as recreated, and want.
func checkRecreatedAre(t *testing.T, step, out string, before, after map[string]string,
	want []string,
) {
	t.Helper()
	got := recreated(before, after)
	listed := planRecreated(out)
	if !slices.Equal(got, listed) || !slices.Equal(got, want) {
		t.Errorf("%s recreated %v, and its plan listed %v; want both %v",
			step, got, listed, want)
	}
}

// planRecreated returns the services under the plan's "Recreating" heading.
func planRecreated(out string) []string {
	var names []string
	in := false
	for line := range strings.Lines(out) {
		switch {
		case strings.HasPrefix(line, "Recreating"):
			in = true
		case in && strings.HasPrefix(line, "  "):
			name, _, _ := strings.Cut(strings.TrimSpace(line), ":")
			names = append(names, name)
		default:
			in = false
		}
	}
	return names
}
