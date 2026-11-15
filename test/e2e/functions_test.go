//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"tule.dev/cli/internal/catalog"
)

// probeImage is the probe function's image, built for this run FROM
// scratch around testdata/probe, so nothing is pulled for it.
var probeImage = "tule-cli-e2e/probe:" + strconv.FormatInt(time.Now().UnixNano(), 36)

// functionsInstanceLabel is on every container functions starts.
const functionsInstanceLabel = "dev.tule.functions.instance"

// buildProbeImage builds testdata/probe as a static linux binary for this
// machine's architecture, and the image around it, in dir.
func buildProbeImage(dir string) error {
	buildDir := filepath.Join(dir, "probe")
	if err := os.MkdirAll(buildDir, 0o750); err != nil {
		return fmt.Errorf("make the build directory: %w", err)
	}
	//nolint:gosec // The e2e test builds its own function.
	build := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(buildDir, "bootstrap"),
		"tule.dev/cli/test/e2e/testdata/probe")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goruntime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	dockerfile := "FROM scratch\nCOPY bootstrap /bootstrap\nENTRYPOINT [\"/bootstrap\"]\n"
	path := filepath.Join(buildDir, "Dockerfile")
	if err := os.WriteFile(path, []byte(dockerfile), 0o600); err != nil {
		return fmt.Errorf("write the Dockerfile: %w", err)
	}
	//nolint:gosec // The e2e test's own docker build.
	image := exec.Command(
		"docker",
		"build",
		"--pull=false",
		"--quiet",
		"--tag",
		probeImage,
		buildDir,
	)
	if out, err := image.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build: %w\n%s", err, out)
	}
	return nil
}

// removeProbeImage removes the probe image, and any container left from it,
// which the tests report as a leak; this only keeps Docker tidy.
func removeProbeImage() {
	out, _ := dockerCommand("ps", "--all", "--quiet", "--filter", "ancestor="+probeImage).Output()
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		_ = dockerCommand(append([]string{"rm", "--force"}, ids...)...).Run()
	}
	_ = dockerCommand("rmi", "--force", probeImage).Run()
}

// dockerCommand is a docker command of the e2e tests' own.
func dockerCommand(args ...string) *exec.Cmd {
	return exec.Command("docker", args...) //nolint:gosec // The e2e test's own docker commands.
}

// probeContainers lists the containers functions runs the probe in.
func probeContainers(t *testing.T) []string {
	t.Helper()
	return strings.Fields(docker(t, "ps", "--all", "--quiet",
		"--filter", "label="+functionsInstanceLabel, "--filter", "ancestor="+probeImage))
}

// lambdaArch is this machine's architecture in Lambda's names.
func lambdaArch() types.Architecture {
	if goruntime.GOARCH == "amd64" {
		return types.ArchitectureX8664
	}
	return types.Architecture(goruntime.GOARCH)
}

// lambdaTrust is the trust policy of Lambda's execution roles.
const lambdaTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
	`"Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// probeResult is what the probe function answers.
type probeResult struct {
	Event       map[string]string `json:"event"`
	RuntimeAPI  string            `json:"runtimeAPI"`
	QueuesError string            `json:"queuesError"`
	QueuesReady int               `json:"queuesReady"`
}

// checkUpFunctions adds functions to a running default-set stack, checks
// only the services wired to it were recreated, and creates and invokes the
// probe function through the Lambda API with an execution role from
// identity.
func checkUpFunctions(t *testing.T, a *stackDir) {
	t.Helper()
	before := containerIDs(t, a.name)
	out := a.must("up", catalog.Functions)
	after := containerIDs(t, a.name)
	checkRecreatedAre(t, "up functions", out, before, after,
		[]string{catalog.ObjectStore, catalog.SecretStore, catalog.Console})
	checkStatus(t, a, catalog.Names(), 0)
	functions := a.name + "-" + catalog.Functions + "-1"
	if got := containerEnv(t, functions)["TULE_Functions_ContainerNetwork"]; got != a.name+"_default" {
		t.Errorf("TULE_Functions_ContainerNetwork = %q, want %s_default", got, a.name)
	}
	if got := docker(t, "port", functions); strings.Contains(got, "9061") {
		t.Errorf("functions publishes the Runtime API on the host: %s", got)
	}

	cfg := a.awsConfig()
	if os.Getenv("AWS_ENDPOINT_URL_LAMBDA") == "" {
		t.Fatalf("tule env set no AWS_ENDPOINT_URL_LAMBDA with functions running:\n%s",
			a.must("env"))
	}
	t.Cleanup(func() {
		if t.Failed() {
			// t.Context is already canceled when cleanups run.
			logs, err := dockerCommand("logs", "--tail", "100", functions).CombinedOutput()
			t.Logf("functions' logs (%v):\n%s", err, logs)
		}
	})
	ctx := t.Context()
	role, err := iam.NewFromConfig(cfg).CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String("e2e-probe"), AssumeRolePolicyDocument: aws.String(lambdaTrust),
	})
	if err != nil {
		t.Fatalf("IAM CreateRole: %v", err)
	}
	lc := lambda.NewFromConfig(cfg)
	if _, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String("e2e-probe"),
		PackageType:   types.PackageTypeImage,
		Code:          &types.FunctionCode{ImageUri: aws.String(probeImage)},
		Role:          role.Role.Arn,
		Architectures: []types.Architecture{lambdaArch()},
	}); err != nil {
		t.Fatalf("Lambda CreateFunction: %v", err)
	}
	if err = lambda.NewFunctionActiveV2Waiter(lc).Wait(ctx,
		&lambda.GetFunctionInput{
			FunctionName: aws.String("e2e-probe"),
		}, 2*time.Minute); err != nil {
		t.Fatalf("wait for the function to be active: %v", err)
	}
	invokeProbe(ctx, t, lc)
	if n := len(probeContainers(t)); n == 0 {
		t.Error("no container ran the probe")
	}
}

// invokeProbe invokes the probe and checks it reached the Runtime API and
// queues by their names on the stack network.
func invokeProbe(ctx context.Context, t *testing.T, lc *lambda.Client) {
	t.Helper()
	out, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String("e2e-probe"), Payload: []byte(`{"hello":"tule"}`),
	})
	if err != nil {
		t.Fatalf("Lambda Invoke: %v", err)
	}
	if out.FunctionError != nil {
		t.Fatalf("the probe failed: %s: %s", aws.ToString(out.FunctionError), out.Payload)
	}
	var got probeResult
	if err := json.Unmarshal(out.Payload, &got); err != nil {
		t.Fatalf("parse the probe's answer %s: %v", out.Payload, err)
	}
	if got.Event["hello"] != "tule" {
		t.Errorf("the probe got the event %v", got.Event)
	}
	if !strings.HasPrefix(got.RuntimeAPI, "functions:9061/") {
		t.Errorf("the probe was given the Runtime API at %q, want functions:9061", got.RuntimeAPI)
	}
	if got.QueuesReady != 200 {
		t.Errorf("the probe reached queues at http://queues:9050/_ready with %d (%s), want 200",
			got.QueuesReady, got.QueuesError)
	}
}
