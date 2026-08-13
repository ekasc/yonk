# Yonk

Yonk lets one machine run work using another machine's CPU and RAM.

```bash
yonk run debian -- pnpm test
```

The command starts on your machine, runs on the selected worker, and returns its output and exit code. Yonk sends the current working tree, including uncommitted changes, so remote execution does not require a commit, push, or clean Git state.

Yonk is built as a general compute layer. A person at a terminal, a CI job, an application, or an agent should all be able to submit the same kind of job through the same protocol.

## Status

Yonk is under active development. The client/server protocol and workspace transfer are working. The current worker executor is deliberately restricted to `echo` while Firecracker isolation is being built.

| Capability | Status |
| --- | --- |
| Worker discovery and capabilities | Working |
| Versioned, platform-aware jobs | Working |
| stdout and stderr streaming | Working |
| Remote exit codes | Working |
| Current working tree transfer | Working |
| Configurable workspace exclusions | Working |
| Temporary workspace cleanup | Working |
| Firecracker/KVM isolation | Planned next |
| CPU, memory, disk, and process limits | Planned |
| General Linux commands | Blocked on isolation |
| Artifacts | Planned |

Do not expose the current daemon to untrusted clients. It does not have application-level authentication yet, and the restricted executor is not a security boundary.

## How it works

```text
CLI
 |
 v
Client
 |
 v
Job protocol
 |
 v
HTTP transport
 |
 v
Worker
 |
 v
Executor
 |
 v
Sandbox / VM
```

Each layer has a narrow responsibility. The job model does not contain Tailscale or Firecracker fields. Tailscale is currently one convenient way to reach a worker; any reachable endpoint can carry the protocol. Firecracker is an executor detail and can be replaced without changing how clients describe jobs.

A run currently follows this path:

1. `yonk` reads the selected worker's capabilities.
2. It packages the current directory as a gzip-compressed tar archive.
3. It uploads the job and workspace to `yonkd`.
4. The worker validates and extracts the archive into a temporary job directory.
5. The executor runs with that directory as its working directory.
6. stdout, stderr, status changes, and the result stream back to the client.
7. The worker removes the job directory before reporting completion.

## Build

Yonk requires Go 1.23 or newer.

```bash
go build -o bin/yonk ./cmd/yonk
go build -o bin/yonkd ./cmd/yonkd
```

Cross-compile the client for an Apple Silicon Mac and the daemon for Debian amd64:

```bash
GOOS=darwin GOARCH=arm64 go build -o bin/yonk ./cmd/yonk
GOOS=linux GOARCH=amd64 go build -o bin/yonkd-linux-amd64 ./cmd/yonkd
```

## Run

Start the daemon on the worker. Use an address reachable from the client, such as the worker's Tailscale IP:

```bash
./yonkd --name debian --listen 100.x.y.z:9665
```

Then run a job from the Mac:

```bash
./yonk run debian -- echo "hello from yonk"
```

You can also use an IP address or URL:

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

The daemon listens on `127.0.0.1:9665` by default. Pass `--listen` to accept remote connections.

## Workspace transfer

Yonk transfers the current working tree, including uncommitted and untracked files. These path components are excluded by default:

```text
.git
node_modules
dist
build
.next
coverage
```

Add exclusions before `--`:

```bash
./yonk run debian \
  --exclude vendor \
  --exclude tmp \
  -- echo "hello from yonk"
```

The worker rejects absolute paths, path traversal, unsafe symlinks, duplicate paths, special files, oversized archives, and excessive file counts during extraction.

## Protocol

The current transport uses HTTP, typed JSON messages, multipart workspace uploads, and newline-delimited JSON event streams.

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/worker` | Return worker identity, resources, platforms, and executors |
| `POST /v1/jobs:run` | Upload a job and workspace, then stream events and the result |
| `POST /v1/jobs/{id}:cancel` | Cancel a running job |

Jobs specify the required platform and resources rather than a sandbox implementation:

```json
{
  "version": 1,
  "id": "job_01...",
  "command": "pnpm",
  "args": ["test"],
  "cwd": ".",
  "platform": {
    "os": "linux",
    "arch": "amd64"
  },
  "resources": {
    "cpu": 4,
    "memory_mb": 4096,
    "disk_mb": 8192
  },
  "timeout_seconds": 300,
  "artifacts": []
}
```

The initial production executor will support `linux/amd64` through Firecracker and KVM. The protocol can represent other platforms without assuming how a worker provides them.

## Security model

Submitted workloads must be treated as malicious. Yonk will not enable general command execution on the host.

The Firecracker executor will put every job in a fresh microVM. The worker will control CPU, memory, disk, process count, runtime, and network access from outside the guest. It will stop the VM and remove all job state after every run.

The current `restricted-host-process` executor runs only `/bin/echo`, without a shell. It exists to support protocol and workspace development until the microVM executor replaces it.

Protecting a workload from a worker is a separate problem. Confidential computing and remote attestation belong later in the roadmap and are not part of the initial system.

## Repository layout

```text
cmd/yonk/          client CLI
cmd/yonkd/         worker daemon
internal/client/   worker protocol client
internal/job/      portable job and event models
internal/worker/   HTTP server and job lifecycle
internal/executor/ executor boundary and current restricted executor
internal/workspace workspace packaging and safe extraction
```

## Roadmap

The immediate priority is Firecracker execution. General commands stay disabled until jobs run inside a microVM with host-enforced limits.

See [ROADMAP.md](ROADMAP.md) for the full plan and acceptance criteria.

## Development

Run the test suite and static checks:

```bash
go test -race ./...
go vet ./...
```
