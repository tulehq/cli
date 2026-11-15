//go:build spike

package spike

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// recordingClient records the URL of every request the SDK sends, so the
// finding can say which addressing style the SDK chose.
type recordingClient struct {
	inner *http.Client
	urls  []string
	mu    sync.Mutex
}

func (c *recordingClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.urls = append(c.urls, req.URL.String())
	c.mu.Unlock()
	//nolint:wrapcheck,gosec // The SDK wraps transport errors; the URL is the spike's own endpoint.
	return c.inner.Do(req)
}

func (c *recordingClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.urls...)
}

// TestDefaultConfigCreateBucket asks whether an SDK client built from
// config.LoadDefaultConfig, with AWS_ENDPOINT_URL_S3 set and no
// UsePathStyle, can CreateBucket and PutObject against object-store, once
// with localhost and once with 127.0.0.1 as the endpoint host.
func TestDefaultConfigCreateBucket(t *testing.T) {
	port := startObjectStore(t, "127.0.0.1")

	for _, host := range []string{"localhost", "127.0.0.1"} {
		t.Run(host, func(t *testing.T) {
			endpoint := "http://" + host + ":" + port
			isolateAWSEnv(t)
			t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AWS_ACCESS_KEY_ID", accessKey)
			t.Setenv("AWS_SECRET_ACCESS_KEY", secretKey)

			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()

			rec := &recordingClient{inner: &http.Client{Timeout: 10 * time.Second}}
			cfg, err := config.LoadDefaultConfig(ctx, config.WithHTTPClient(rec))
			if err != nil {
				t.Fatalf("load the default config: %v", err)
			}
			client := s3.NewFromConfig(cfg)

			bucket := "spike-" + strings.ReplaceAll(host, ".", "-")
			err = createAndPut(ctx, client, bucket)
			urls := rec.recorded()
			t.Logf("request URLs: %v", urls)
			if err != nil {
				t.Logf("FINDING endpoint %s: FAILED: %v", endpoint, err)
				return
			}
			t.Logf("FINDING endpoint %s: %s", endpoint, styleFinding(urls, bucket))
		})
	}
}

func createAndPut(ctx context.Context, client *s3.Client, bucket string) error {
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		return err //nolint:wrapcheck // Logged as the finding, unwrapped.
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("hello.txt"),
		Body:   strings.NewReader("hello"),
	})
	return err //nolint:wrapcheck // Logged as the finding, unwrapped.
}

// styleFinding names the addressing style the successful requests used: the
// bucket in the host is virtual-hosted style, in the path is path style.
func styleFinding(urls []string, bucket string) string {
	for _, u := range urls {
		if strings.Contains(u, "//"+bucket+".") {
			return "VIRTUAL-HOSTED OK"
		}
	}
	return "PATH-STYLE OK"
}

// isolateAWSEnv keeps the runner's own AWS profile, shared files and
// endpoint settings out of the default config chain.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, key := range []string{
		"AWS_PROFILE",
		"AWS_DEFAULT_PROFILE",
		"AWS_SESSION_TOKEN",
		"AWS_ENDPOINT_URL",
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS",
		"AWS_DEFAULT_REGION",
	} {
		t.Setenv(key, "") // Restores the value after the test.
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
}
