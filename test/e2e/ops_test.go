//go:build e2e

package e2e

import (
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"tule.dev/cli/internal/catalog"
)

// checkEnvReadBack checks tule env prints the same variables from a
// directory without the stack's tule.yaml, read back from the containers.
func checkEnvReadBack(t *testing.T, a *stackDir) {
	t.Helper()
	home, homeErr := a.tuleEnv(a.dir)
	elsewhere, stderr := a.tuleEnv(t.TempDir())
	if elsewhere != home {
		t.Errorf("tule env elsewhere:\n%s\nin the stack's directory:\n%s", elsewhere, home)
	}
	for _, e := range []string{homeErr, stderr} {
		if !strings.Contains(e, "read back from running stack "+a.name) {
			t.Errorf("tule env stderr = %q, want it to say it read the stack back", e)
		}
	}
}

// checkLogs checks tule logs prints a service's logs from any directory.
func checkLogs(t *testing.T, a *stackDir) {
	t.Helper()
	out, err := a.tuleIn(t.TempDir(), "logs", "--since", "1h", catalog.Queues)
	if err != nil {
		t.Fatalf("tule logs queues: %v\n%s", err, out)
	}
	if !strings.Contains(out, "queues-1") {
		t.Errorf("tule logs queues printed no line of queues:\n%s", out)
	}
}

// checkRestartKeepsData restarts the stack from any directory, and checks
// its containers were restarted, not recreated, and the data made before is
// still there.
func checkRestartKeepsData(t *testing.T, a *stackDir) {
	t.Helper()
	before := containerIDs(t, a.name)
	out, err := a.tuleIn(t.TempDir(), "restart")
	if err != nil {
		t.Fatalf("tule restart: %v\n%s", err, out)
	}
	if after := containerIDs(t, a.name); !maps.Equal(before, after) {
		t.Errorf("restart recreated containers: before %v, after %v", before, after)
	}
	checkStatus(t, a, catalog.DefaultSelection().Names(), 0)
	cfg := a.awsConfig()
	if _, err := s3.NewFromConfig(cfg).HeadObject(t.Context(), &s3.HeadObjectInput{
		Bucket: aws.String("e2e-a"), Key: aws.String("hello.txt"),
	}); err != nil {
		t.Errorf("S3 HeadObject after restart: %v", err)
	}
	if _, err := sqs.NewFromConfig(cfg).GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{
		QueueName: aws.String("e2e"),
	}); err != nil {
		t.Errorf("SQS GetQueueUrl after restart: %v", err)
	}
}

// checkReset checks reset refuses without a terminal or --yes, and with
// --yes recreates mailbox alone with an empty volume.
func checkReset(t *testing.T, a *stackDir) {
	t.Helper()
	ses := sesv2.NewFromConfig(a.awsConfig())
	if n := emailIdentities(t, ses); n == 0 {
		t.Fatal("mailbox has no email identity before the reset")
	}
	before := containerIDs(t, a.name)
	out, err := a.tule("reset", catalog.Mailbox)
	if err == nil || !strings.Contains(out, "pass --yes") {
		t.Errorf("reset without a terminal or --yes: %v\n%s", err, out)
	}
	if after := containerIDs(t, a.name); !maps.Equal(before, after) {
		t.Errorf("the refused reset changed containers: before %v, after %v", before, after)
	}
	a.must("reset", catalog.Mailbox, "--yes")
	after := containerIDs(t, a.name)
	if got := recreated(before, after); len(got) != 1 || got[0] != catalog.Mailbox {
		t.Errorf("reset mailbox recreated %v, want mailbox alone", got)
	}
	checkStatus(t, a, catalog.DefaultSelection().Names(), 0)
	if n := emailIdentities(t, ses); n != 0 {
		t.Errorf("mailbox has %d email identities after the reset, want none", n)
	}
}

func emailIdentities(t *testing.T, ses *sesv2.Client) int {
	t.Helper()
	out, err := ses.ListEmailIdentities(t.Context(), &sesv2.ListEmailIdentitiesInput{})
	if err != nil {
		t.Fatalf("SES ListEmailIdentities: %v", err)
	}
	return len(out.EmailIdentities)
}
