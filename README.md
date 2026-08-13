# Yonk

Yonk is a general-purpose remote compute fabric. The repository currently implements the remote job protocol and workspace-transfer foundation: the client packages its current working tree, uploads it to a temporary worker directory, and streams execution events back. The temporary executor still permits only `echo`.

```text
CLI → protocol client → HTTP/NDJSON → worker → restricted executor
```

The network is represented only by an endpoint. Yonk has no Tailscale-specific code; a Tailscale hostname or IP is simply one possible endpoint.

## Security status

> **Do not expose this milestone daemon to untrusted clients.**

The temporary `restricted-host-process` executor runs `/bin/echo` directly on the worker host without a shell. It rejects every other command. This deliberately narrow executor exists only to prove protocol flow. It is **not a sandbox** and will be removed when the Firecracker executor lands.

`yonkd` binds to `127.0.0.1:9665` by default and has no application-layer authentication yet. Bind it to a remote interface only on a network whose peers you trust and whose access controls you have configured. The eventual general-command executor must use Firecracker/KVM and provider-controlled resource limits; broad host execution will not be added.

## Build

Go 1.23 or newer is required.

```bash
go build ./cmd/yonk
go build ./cmd/yonkd
```

Cross-compile from macOS for the initial machines:

```bash
GOOS=darwin GOARCH=arm64 go build -o bin/yonk ./cmd/yonk
GOOS=linux GOARCH=amd64 go build -o bin/yonkd ./cmd/yonkd
```

## Run the current milestone

On Debian, choose an address reachable from the Mac (for example, the machine's Tailscale IP):

```bash
./yonkd --name debian --listen 100.x.y.z:9665
```

On the Mac, a Tailscale MagicDNS hostname works as a normal endpoint:

```bash
./yonk run debian -- echo "hello from yonk"
```

Or specify an address/URL explicitly:

```bash
./yonk run 100.x.y.z:9665 -- echo "hello from yonk"
./yonk run http://100.x.y.z:9665 -- echo "hello from yonk"
```

Expected output:

```text
worker: debian
syncing workspace...
hello from yonk
exit: 0
```

Any command other than the exact command name `echo` is rejected by the worker.

The current directory is archived with `tar` and gzip, including uncommitted files. These path components are excluded by default wherever they occur:

```text
.git  node_modules  dist  build  .next  coverage
```

Add exclusions before the command separator:

```bash
./yonk run debian --exclude vendor --exclude tmp -- echo "hello from yonk"
```

The worker validates archive paths, links, file types, expanded size, compressed size, and file count. It removes the temporary job directory before sending the completion event.

## Protocol

Yonk uses standard HTTP with typed JSON messages and newline-delimited JSON event streaming:

- `GET /v1/worker` — worker identity, resources, platform, and executors
- `POST /v1/jobs:run` — upload job JSON plus a `tar.gz` workspace as multipart data, then stream status, stdout, stderr, failure, and completion events
- `POST /v1/jobs/{id}:cancel` — cancel a running job

The request context provides cancellation if the client disconnects. Jobs are explicitly versioned and platform-aware; portable job fields contain no Firecracker, KVM, or Tailscale details.

## Current boundaries

- `cmd/yonk` — CLI
- `internal/client` — protocol client and endpoint handling
- `internal/job` — portable protocol model
- `internal/worker` — HTTP worker service and event stream
- `internal/executor` — replaceable executor boundary and restricted milestone implementation
- `internal/workspace` — workspace packaging, exclusions, bounded extraction, and archive safety
- `cmd/yonkd` — daemon lifecycle and host capability collection

## Next milestones

1. Replace the restricted executor with an ephemeral Firecracker/KVM executor and place the uploaded workspace inside the guest.
2. Enforce CPU, memory, disk, process, network, and runtime limits outside the guest; add hostile-workload tests.
3. Run a real Linux development workload and return richer telemetry/artifacts.

Firecracker, general commands, artifact handling, and resource isolation are intentionally not implemented yet.

## Test

```bash
go test -race ./...
go vet ./...
```
