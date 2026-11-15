//go:build spike

// Package spike answers two questions about the runtime the CLI will build
// on, against a real Docker daemon. Each test logs its finding and passes
// whichever way the answer goes; a test fails only when it could not ask.
package spike

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	objectStoreImage = "ghcr.io/tulehq/object-store:0.13.0"
	probeImage       = "alpine:3.24"
	accessKey        = "test"
	secretKey        = "test"
)

// docker runs the docker CLI and returns its trimmed stdout. The error
// carries stderr.
func docker(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // The spike builds its own arguments.
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf(
			"docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()),
		)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// startObjectStore runs object-store with its port 9000 published on
// bindIP and a host port Docker picks, waits until /_ready answers 200 on
// 127.0.0.1 from the host, and returns that host port.
func startObjectStore(t *testing.T, bindIP string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	id, err := docker(ctx, "run", "-d",
		"-p", bindIP+"::9000",
		"-e", "TULE_Auth_AccessKeyID="+accessKey,
		"-e", "TULE_Auth_SecretAccessKey="+secretKey,
		objectStoreImage,
	)
	if err != nil {
		t.Fatalf("start object-store: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is already canceled when cleanups run.
		rmCtx, rmCancel := context.WithTimeout(context.Background(), time.Minute)
		defer rmCancel()
		if _, rmErr := docker(rmCtx, "rm", "-f", id); rmErr != nil {
			t.Errorf("remove object-store container: %v", rmErr)
		}
	})

	mapping, err := docker(ctx, "port", id, "9000/tcp")
	if err != nil {
		t.Fatalf("read the published port: %v", err)
	}
	// One line per binding, such as "127.0.0.1:32768".
	first, _, _ := strings.Cut(mapping, "\n")
	_, port, err := net.SplitHostPort(strings.TrimSpace(first))
	if err != nil {
		t.Fatalf("parse the published port from %q: %v", mapping, err)
	}
	t.Logf("object-store %s published on %s:%s", id[:12], bindIP, port)

	waitReady(ctx, t, id, "http://127.0.0.1:"+port+"/_ready")
	return port
}

func waitReady(ctx context.Context, t *testing.T, id, url string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	var last string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			t.Fatalf("build the readiness request: %v", err)
		}
		res, err := client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
			last = res.Status
		} else {
			last = err.Error()
		}

		select {
		case <-ctx.Done():
			// ctx is done, so the logs need a context of their own.
			logsCtx, logsCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			logs, logsErr := docker(logsCtx, "logs", id)
			logsCancel()
			if logsErr != nil {
				logs = logsErr.Error()
			}
			t.Fatalf(
				"object-store not ready at %s from the host (last: %s); logs:\n%s",
				url,
				last,
				logs,
			)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
