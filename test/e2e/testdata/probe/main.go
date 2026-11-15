// Command probe is the function the e2e tests run in functions. It speaks
// the Lambda Runtime API itself, with the standard library alone, so its
// image is built FROM scratch around it and nothing is pulled. Each
// invocation asks queues for readiness by its service name, which only a
// container on the stack network resolves, and answers what it found.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// queuesReady is queues' readiness check, by its name on the stack network.
const queuesReady = "http://queues:9050/_ready"

// result is what an invocation answers.
type result struct {
	Event       json.RawMessage `json:"event"`
	RuntimeAPI  string          `json:"runtimeAPI"`
	QueuesError string          `json:"queuesError,omitempty"`
	QueuesReady int             `json:"queuesReady"`
}

func main() {
	api := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API") + "/2018-06-01/runtime"
	for {
		if err := serve(api); err != nil {
			fmt.Fprintln(os.Stderr, "probe:", err)
			os.Exit(1)
		}
	}
}

// serve takes the next invocation and answers it.
func serve(api string) error {
	resp, err := http.Get(api + "/invocation/next") //nolint:noctx // Waits as long as there is no event.
	if err != nil {
		return fmt.Errorf("next invocation: %w", err)
	}
	event, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read the event: %w", err)
	}
	id := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
	out := result{Event: event, RuntimeAPI: os.Getenv("AWS_LAMBDA_RUNTIME_API")}
	out.QueuesReady, err = probeQueues()
	if err != nil {
		out.QueuesError = err.Error()
	}
	body, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode the result: %w", err)
	}
	answer, err := http.Post(api+"/invocation/"+id+"/response", //nolint:noctx // A short call.
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("answer: %w", err)
	}
	_ = answer.Body.Close()
	return nil
}

// probeQueues returns the status queues' readiness check answers.
func probeQueues() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queuesReady, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
