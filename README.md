# tule

A command-line tool that runs a local [Tule](https://github.com/tulehq)
stack: the drop-in replacements for AWS services (object-store for S3,
queues for SQS, and the rest) that Tule publishes as images. It renders a
compose file for the services you pick and runs it with `docker compose`.

**Status: under construction.** Until it ships, run a stack with the compose
file in [tulehq/console](https://github.com/tulehq/console).

## Install

Once v0.1.0 is out, this will work:

```sh
go install tule.dev/cli/cmd/tule@latest
```

Then `tule version` prints the version you installed.

## Commands

`tule` runs a stack with `docker compose`, so it needs the Docker Compose
v2 plugin, which Docker Desktop includes and Linux installs as the
`docker-compose-plugin` package. `logs`, `restart` and `reset` come next.

| Command | Does |
|---|---|
| `tule up [service...]` | Starts the stack and waits until its services are healthy. A new stack runs the services named, or the configured ones, or the default set; on a running stack, the services named are added. Lists the services recreated because their wiring or version changes before acting |
| `tule down [service...] [--volumes]` | With services named, removes them from the stack, and recreates the services wired to them without their endpoints. With none, removes the whole stack. `--volumes` also deletes the data |
| `tule status [--json]` | Shows each service's state, health, version, host address and volume. Exits non-zero when a service is not running and healthy |
| `tule services` | Lists the catalog: each service, the AWS service it stands in for, its image and tested version, its default ports |
| `tule compose [service...] [-o file]` | Writes the compose file for the services named, or the configured ones, or the default set: every service but functions |
| `tule env [--shell sh\|fish\|powershell]` | Prints the credentials, region and endpoint variables that point AWS SDKs and the AWS CLI at the configured services |
| `tule version` | Prints the version |

Every command takes `--stack <name>` (default `tule`), the compose project
name that namespaces containers, network and volumes, and `--config
<file>`, the configuration to read instead of `tule.yaml`.

### tule up, down and status

```sh
tule up object-store queues # only these
eval "$(tule env)"
tule status
tule up identity            # adds identity, and rewires the others to it
tule down queues            # removes queues, and its endpoint from the others
tule down                   # removes the stack; its data stays in its volumes
tule up                     # on a new stack: the default set, every service but functions
tule down --volumes         # removes the stack and deletes its data
```

`up` runs the same compose file `tule compose` writes, as
`docker compose -p <stack> -f - up -d --wait`. Before it acts, it lists
the version changes, the running services it recreates because their
wiring or host ports change (by variable name; values are not printed), and
the services it starts or removes. It also checks every host port it
publishes is free, and names the service and the `ports:` or `portOffset:`
key to change when one is not. The check listens on this machine, so it
cannot see ports on a remote Docker host; there, pass `--skip-port-check`.

Compose does not read a `.env` file in the directory `tule` runs from:
`tule` gives it an empty env file instead, so the stack is the rendered
file and nothing else.

Nothing is kept on disk. The stack's services are the catalog services
that have containers in its compose project, running or stopped, and each
container carries the absolute path of the `tule.yaml` it was started from
as the label `dev.tule.config` (empty for none). So `status`, and `down`
of the whole stack, work from any directory. `up`, and `down` with
services named, re-render the stack from its configuration, and refuse
when the running stack was started from another `tule.yaml`, naming both.
On a running stack, a bare `up` keeps the stack's services and warns when
`services:` in `tule.yaml` names others: add services with `tule up
<service...>`, and remove them with `tule down <service...>`.

Volumes carry no tule labels, so a volume compose created from console's
compose.yaml, under the same project name, is used as it is. Version
changes are listed, not checked: a downgrade, which can serve stale data,
is not refused yet, and after a `down` without `--volumes` the next `up`
does not know which version wrote the kept data. `down --volumes` deletes the volumes of
the catalog services only, not other volumes in the compose project.

`tule up` does not run functions yet; write its compose file with `tule
compose` and run it with `docker compose`. `tule down` also removes the
function containers functions left on the stack's network
(`dev.tule.functions.instance`).

Two stacks run side by side under different `--stack` names, the second
with a `portOffset` in its `tule.yaml`:

```sh
echo 'portOffset: 100' > other/tule.yaml
(cd other && tule --stack other up object-store)  # object-store on 9100
```

### tule compose

```sh
tule compose                          # the default set, to standard output
tule compose object-store queues -o compose.yaml
docker compose -f compose.yaml up -d
```

Services are wired only to the services selected with them: object-store
gets queues' endpoint only when queues is selected, and every service
accepts the keys identity issues only when identity is selected. The
console needs at least one other service.

functions is never in the default set: it mounts the Docker socket, which
is root-equivalent on the host. Name it to include it, and give it the
group ID that owns the socket on the Docker host with `--docker-gid`, which
is required with functions. On Linux:

```sh
tule compose functions queues --docker-gid "$(stat -c %g /var/run/docker.sock)"
```

With Docker Desktop (macOS, Windows), the socket functions sees is the one
inside Docker Desktop's VM, whose group is not the one `stat` reports on the
host; this prints it:

```sh
docker run --rm --entrypoint stat -v /var/run/docker.sock:/s ghcr.io/tulehq/functions:0.7.0 -c %g /s
```

functions' README marks the Docker Desktop setup as untried. Function containers reach functions' Runtime
API through the host, which on a native Linux daemon does not answer on a
port published only on 127.0.0.1; until functions can attach them to the
stack network, a functions stack on Linux needs `bind: 0.0.0.0`, and so a
`rootKey` of your own.

### tule env

```sh
eval "$(tule env)"
tule env --shell fish | source
tule env --shell powershell | Out-String | Invoke-Expression
```

In PowerShell, keep `Out-String`: without it, `Invoke-Expression` gets the
output a line at a time. `tule env` refuses to print a value that holds a
newline or another control character, in every shell.

It sets `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` and
`AWS_DEFAULT_REGION`, unsets `AWS_SESSION_TOKEN`, and sets an
`AWS_ENDPOINT_URL_<SERVICE>` variable for each configured service:

| Service | Variables |
|---|---|
| identity | `AWS_ENDPOINT_URL_IAM`, `AWS_ENDPOINT_URL_STS` |
| accounts | `AWS_ENDPOINT_URL_ORGANIZATIONS` |
| object-store | `AWS_ENDPOINT_URL_S3` |
| secret-store | `AWS_ENDPOINT_URL_SECRETS_MANAGER` |
| mailbox | `AWS_ENDPOINT_URL_SES`, `AWS_ENDPOINT_URL_SESV2` (mailbox serves SES v2 only; an SES v1 client fails locally rather than reaching AWS) |
| queues | `AWS_ENDPOINT_URL_SQS` |
| functions | `AWS_ENDPOINT_URL_LAMBDA` |

Endpoints use `publicHost`. SDKs address buckets path style at an IP
address and virtual-hosted style (`<bucket>.<host>`) at a host name, so the
S3 endpoint uses `127.0.0.1` when `publicHost` is `localhost`, and
`publicHost` itself otherwise. With a `publicHost` that is a host name other
than `localhost`, S3 clients need path-style addressing (`UsePathStyle` in
the Go SDK, `s3.addressing_style = path` in the AWS CLI and boto3
configuration) unless wildcard DNS resolves `<bucket>.<host>` to the stack.

The SDK for Go 1.x (V1), the SDK for Java 1.x and the SDK for JavaScript
2.x do not read these variables, per the support table in the AWS SDKs and
Tools Reference Guide's
[Service-specific endpoints](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html#ss-endpoints-sdk-compat);
configure their endpoints in code.

## tule.yaml

`tule` reads `tule.yaml` from the working directory or the nearest parent,
or the file `--config` names. Every key is optional:

```yaml
services: [object-store, queues, identity]  # default: every service but functions
ports: {object-store: 9000}       # a service's API host port
portOffset: 0                     # added to every default host port, for a second stack
versions: {object-store: 0.13.1}  # default: the tested version; another one warns
rootKey: {id: AKIAIOSFODNN7EXAMPLE, secret: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY}
bind: 127.0.0.1                   # the address host ports are published on
publicHost: localhost             # the host clients use: queue URLs and tule env
env: {object-store: {TULE_Server_Domain: localhost}}  # extra variables, set over the wiring
```

- Services named on the command line replace `services`.
- `ports` sets a service's API port; functions' Runtime API port, 9061,
  moves with `portOffset` only.
- `versions` takes release versions: `0.13.1`, or with a prerelease,
  `0.14.0-rc.1`.
- `rootKey` defaults to the AWS documentation's example key above. Its id
  and secret may hold only letters, digits and `+ / = . _ -`. The root key
  is set only here: `env` refuses `TULE_Auth_AccessKeyID` and
  `TULE_Auth_SecretAccessKey`.
- A `bind` other than a loopback address needs a `rootKey` whose id and
  secret are both your own.
- Unknown keys and service names are errors.

## Development

```sh
make        # lint, test and build
make fmt
make test
make build     # writes build/tule
make lint
make test-e2e  # stacks from the released images on the local Docker daemon
```
