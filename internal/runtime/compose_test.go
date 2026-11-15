package runtime

import (
	"reflect"
	"strings"
	"testing"

	"tule.dev/cli/internal/render"
)

// inspectOutput is `docker container inspect` output trimmed to the fields
// PS reads, for one healthy container and one without a health check.
const inspectOutput = `[
  {
    "Id": "aaa",
    "Name": "/tule-queues-1",
    "Image": "sha256:img1",
    "Config": {
      "Image": "ghcr.io/tulehq/queues:0.6.1",
      "Env": ["TULE_Queues_BaseURL=http://localhost:9050", "PATH=/usr/bin", "EMPTY="],
      "Labels": {"dev.tule.service": "queues", "dev.tule.selection": "object-store,queues"}
    },
    "State": {"Status": "running", "Health": {"Status": "healthy"}},
    "NetworkSettings": {"Ports": {
      "9050/tcp": [{"HostIp": "127.0.0.1", "HostPort": "9050"}],
      "9999/udp": null
    }},
    "Mounts": [
      {"Type": "volume", "Name": "tule_queuesdata", "Destination": "/data"},
      {"Type": "bind", "Source": "/tmp", "Destination": "/tmp"}
    ]
  },
  {
    "Id": "bbb",
    "Name": "/tule-console-1",
    "Image": "sha256:img2",
    "Config": {"Image": "ghcr.io/tulehq/console:0.3.9", "Env": null, "Labels": {}},
    "State": {"Status": "exited", "Health": null},
    "NetworkSettings": {"Ports": {}},
    "Mounts": []
  }
]`

func TestParseContainers(t *testing.T) {
	got, images, err := parseContainers([]byte(inspectOutput))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(images, []string{"sha256:img1", "sha256:img2"}) {
		t.Errorf("images = %v", images)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers", len(got))
	}
	queues := got[0]
	want := Container{
		ID:     "aaa",
		Name:   "tule-queues-1",
		Image:  "ghcr.io/tulehq/queues:0.6.1",
		State:  "running",
		Health: "healthy",
		Labels: map[string]string{
			"dev.tule.service":   "queues",
			"dev.tule.selection": "object-store,queues",
		},
		Env: map[string]string{
			"TULE_Queues_BaseURL": "http://localhost:9050",
			"PATH":                "/usr/bin",
			"EMPTY":               "",
		},
		Ports:  []Port{{HostIP: "127.0.0.1", HostPort: 9050, Container: 9050}},
		Mounts: []Mount{{Name: "tule_queuesdata", Destination: "/data"}},
	}
	if !reflect.DeepEqual(queues, want) {
		t.Errorf("queues =\n%+v\nwant\n%+v", queues, want)
	}
	if got[1].Health != "" || got[1].State != "exited" || len(got[1].Ports) != 0 {
		t.Errorf("console = %+v", got[1])
	}
}

func TestParseContainersRejectsABadPort(t *testing.T) {
	bad := strings.Replace(inspectOutput, `"HostPort": "9050"`, `"HostPort": "x"`, 1)
	if _, _, err := parseContainers([]byte(bad)); err == nil {
		t.Error("parseContainers() accepted a host port that is not a number")
	}
}

func TestParseImagesAndVolumes(t *testing.T) {
	images, err := parseImages([]byte(`[{"Id": "sha256:img1", "Config": {"Env": ["PATH=/bin"]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if images["sha256:img1"]["PATH"] != "/bin" {
		t.Errorf("images = %v", images)
	}
	volumes, err := parseVolumes([]byte(`[{"Name": "tule_queuesdata", "Labels": {
		"com.docker.compose.volume": "queuesdata", "dev.tule.version": "0.6.1"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].Name != "tule_queuesdata" ||
		volumes[0].Key() != "queuesdata" {
		t.Errorf("volumes = %+v", volumes)
	}
}

func TestCheckComposeVersion(t *testing.T) {
	for _, ok := range []string{"2.40.3\n", "v2.20.0", "5.0.1"} {
		if err := checkComposeVersion([]byte(ok)); err != nil {
			t.Errorf("checkComposeVersion(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"1.29.2", "", "docker-compose"} {
		err := checkComposeVersion([]byte(bad))
		if err == nil || !strings.Contains(err.Error(), "Compose v2 plugin") {
			t.Errorf("checkComposeVersion(%q) = %v, want an error naming the plugin", bad, err)
		}
	}
}

func TestComposeArgs(t *testing.T) {
	p := &render.Project{Name: "dev"}
	got := composeArgs(p, "up", "--detach")
	want := []string{"compose", "--project-name", "dev", "--file", "-", "up", "--detach"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("composeArgs() = %v, want %v", got, want)
	}
	logs := logsArgs(LogsOptions{Follow: true, Since: "10m", Services: []string{"queues"}})
	if !reflect.DeepEqual(logs, []string{"logs", "--follow", "--since", "10m", "queues"}) {
		t.Errorf("logsArgs() = %v", logs)
	}
}
