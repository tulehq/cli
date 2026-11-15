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

## Development

```sh
make        # lint, test and build
make fmt
make test
make build  # writes build/tule
make lint
```
