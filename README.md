# Yonk

Yonk runs a command on another machine's CPU and RAM.

```bash
yonk run worker -- pnpm test
```

The command starts on your machine, runs on the worker you select, and streams
back the output and exit code. Yonk sends your current working tree, including
uncommitted and untracked files, so remote execution does not require a commit,
push, or clean Git state.

> **Experimental (pre-alpha).** Yonk is under active development. The daemon
> has no application-level authentication and serves plain HTTP. Do not expose
> `yonkd` to untrusted clients or the public internet, and do not treat it as
> production software. Interfaces and the wire protocol may change without
> notice. See [SECURITY.md](SECURITY.md) and [Limitations](#limitations).

## Why Yonk

- Run tests, builds, or any command on a faster or different machine without
  changing your workflow.
- Transfer the working tree as-is, with no Git operations required.
- Describe a job by what it needs: OS, architecture, CPU, memory, disk,
  timeout, network. How a worker provides that is its own concern.
- Run each job in a fresh Firecracker microVM with limits enforced from the
  host, not directly on the worker.

Yonk is a general compute layer: a person at a terminal, a CI job, an
application, or an agent should all be able to submit the same kind of job
through the same protocol.

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

Each layer has a narrow responsibility. The job model contains no transport or
executor fields: Tailscale is one convenient way to reach a worker, and any
reachable endpoint can carry the protocol. Firecracker is an executor detail
that can be replaced without changing how clients describe jobs.

A run follows this path:

1. `yonk` reads the selected worker's capabilities.
2. It packages the current directory as a gzip-compressed tar archive.
3. It uploads the job and workspace to `yonkd`.
4. The worker validates and extracts the archive into a temporary job directory.
5. The executor runs the job with that directory as its working directory.
6. stdout, stderr, status changes, and the result stream back to the client.
7. The worker removes the job directory before reporting completion.

## Requirements

- **Client** (macOS or Linux): the `yonk` binary, or Go 1.25+ to build it.
- **Worker**: Linux amd64 with `/dev/kvm`, `mkfs.ext4`, and cgroup v2 for
  isolated jobs. On a host without KVM, the worker falls back to a restricted
  executor that only permits `echo`, enough to try the protocol end to end.
- **Network path** from client to worker. Tailscale is a convenient option; any
  reachable endpoint works.

## Install

Pre-alpha releases are not published yet, so install from source (Go 1.25+):

```bash
go install github.com/ekasc/yonk/cmd/yonk@latest
go install github.com/ekasc/yonk/cmd/yonkd@latest
```

Or build from a checkout:

```bash
go build -o bin/yonk ./cmd/yonk
go build -o bin/yonkd ./cmd/yonkd
```

Cross-compile the client for an Apple Silicon Mac and the daemon for Debian
amd64:

```bash
GOOS=darwin GOARCH=arm64 go build -o bin/yonk ./cmd/yonk
GOOS=linux GOARCH=amd64 go build -o bin/yonkd-linux-amd64 ./cmd/yonkd
```

The isolated worker also needs the guest agent and a toolchain rootfs. See
[Worker setup](#worker-setup).

## Quick start

Try the protocol with the restricted executor. This runs anywhere, including a
laptop without KVM, and only permits `echo`:

```bash
./yonkd --executor restricted --name local --listen 127.0.0.1:9665
```

In another terminal:

```bash
./yonk run local -- echo "hello from yonk"
```

Expected output:

```text
worker: local
syncing workspace...
hello from yonk
duration: 0.0s
exit: 0
```

To run real workloads, set up a Linux/KVM worker next.

## Worker setup

On a Debian worker, install the core assets and build the toolchain rootfs (run
from the repository root as root):

```bash
sudo ./scripts/setup-worker.sh    # Firecracker, kernel, yonk-guest agent
sudo ./scripts/build-rootfs.sh    # read-only rootfs with Go, Node, pnpm, gcc, git, make
```

The setup script downloads Firecracker and a guest kernel into `/opt/yonk` and
builds the static `yonk-guest` agent. The rootfs script debootstraps a minimal
Debian, installs the toolchains, bakes the agent in, and writes
`/opt/yonk/rootfs.ext4` (immutable and shared across jobs). It requires `curl`,
`tar`, `go`, `debootstrap`, `e2fsprogs`, and `/dev/kvm`. After toolchain
changes, `sudo ./scripts/rebake-rootfs.sh <staging>` refreshes the image without
re-running debootstrap.

Start `yonkd` with the microVM executor:

```bash
sudo yonkd --executor microvm \
  --listen 100.x.y.z:9665 \
  --firecracker-bin /opt/yonk/firecracker \
  --kernel /opt/yonk/vmlinux.bin \
  --rootfs /opt/yonk/rootfs.ext4
```

The default is `--executor auto`, which uses the microVM executor when KVM and
all assets are available and falls back to the restricted host executor
otherwise. `--executor microvm` fails loudly instead of falling back.

`yonkd` requires root, KVM (`/dev/kvm`), cgroup v2, `mkfs.ext4`, and a writable
VM work directory (`--vm-work-dir`). Egress jobs also require nftables and
`/dev/net/tun`.

## Running jobs

Start the daemon on the worker with an address reachable from the client, such
as the worker's Tailscale IP:

```bash
./yonkd --name debian --listen 100.x.y.z:9665
```

Then run a job from the client:

```bash
./yonk run debian -- echo "hello from yonk"
```

The daemon listens on `127.0.0.1:9665` by default. Pass `--listen` to accept
remote connections. You can address a worker by name, `host:port`, or URL:

```bash
./yonk run 100.x.y.z:9665 -- echo "hello from yonk"
./yonk run http://100.x.y.z:9665 -- echo "hello from yonk"
```

Resource requests and the timeout are set per job:

```bash
./yonk run debian --cpu 4 --memory-mb 4096 --disk-mb 8192 --timeout 300 -- go test ./...
```

The worker clamps each request to its provider ceilings, configured on `yonkd`
with `--max-vcpu`, `--max-memory-mb`, and `--max-disk-mb` (default 8192 MiB).
Requests are never allowed to exceed those ceilings.

Request workspace-relative files back after the job:

```bash
./yonk run debian --artifact dist/app.js -- pnpm build
```

Returned artifacts are written into the current directory using the file's base
name, one file per requested path, and each artifact is limited to 512 MiB.
Only artifacts that were requested are accepted from the worker.

## Workspace transfer

Yonk transfers the current working tree, including uncommitted and untracked
files. These path components are excluded by default:

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

Pass `--no-default-excludes` to transfer everything, including the
default-excluded directories.

The worker rejects absolute paths, path traversal, unsafe symlinks, duplicate
paths, special files, oversized archives, and excessive file counts during
extraction.

## Job networking

Jobs have **no network access by default**. For workloads that need to reach the
internet (installs, module downloads):

```bash
./yonk run debian --network egress -- pnpm install
./yonk run debian --network egress -- go mod download
```

Egress is controlled and minimal: jobs reach public destinations only. Host-side
nftables rules drop all inbound traffic from job taps (jobs cannot reach the
worker's SSH, `yonkd`, or other services) and drop private, CGNAT (Tailscale),
link-local, benchmarking, documentation, and reserved destinations (jobs cannot
reach the provider LAN or other tailnet machines). IPv6 is disabled, each job
gets an isolated `/30` and TAP, and the worker rate-limits per-job bandwidth and
packets (`--max-egress-mbps`, `--max-egress-pps`). The worker also provisions the
guest's resolver (`--guest-resolver`).

## Security model

Submitted workloads must be treated as malicious. Yonk does not enable general
command execution on the worker host.

The Firecracker executor puts every job in a fresh microVM with no network
device by default; jobs opt into controlled egress with `--network egress`. The
worker clamps vCPU, memory, and disk to provider ceilings, controls runtime from
outside the guest, then stops the VM and removes all job state after every run.

The `restricted-host-process` executor is only a no-KVM fallback and permits
just `echo`; general commands never run directly on the host.

Protecting a workload from the worker that runs it is a separate problem.
Confidential computing and remote attestation are not part of the current
design. See [SECURITY.md](SECURITY.md) to report a vulnerability.

## Limitations

- The daemon has no application-level authentication and serves plain HTTP. Do
  not expose it to untrusted clients; restrict access at the network layer (for
  example, to a Tailscale interface).
- `--executor auto` falls back to the restricted host executor when KVM or the
  Firecracker assets are unavailable. The restricted executor runs only `echo`
  and is not an isolation boundary.
- The Firecracker process runs without the jailer. Host enforcement relies on
  `yonkd`'s cgroup limits, VM teardown, and the microVM boundary rather than the
  jailer's chroot and namespaces.
- Artifacts are single files, up to 512 MiB each.
- No released versions yet; the protocol and CLI may change without notice.

## Protocol

The current transport uses HTTP, typed JSON messages, multipart workspace
uploads, and newline-delimited JSON event streams.

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/worker` | Return worker identity, resources, platforms, and executors |
| `POST /v1/jobs:run` | Upload a job and workspace, then stream events and the result |
| `POST /v1/jobs/{id}:cancel` | Cancel a running job |

Jobs specify the required platform and resources rather than a sandbox
implementation:

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
  "artifacts": [],
  "network": "none"
}
```

The production executor supports `linux/amd64` through Firecracker and KVM. The
protocol can represent other platforms without assuming how a worker provides
them.

## Project status

Yonk is under active development. The client/server protocol, workspace
transfer, the Firecracker microVM executor, provider-enforced limits, and
controlled job networking are working. MicroVM features are validated on
Linux/amd64 with KVM; the protocol and CLI run on any supported platform. The
restricted `echo` fallback is the only executor available without KVM. Below,
"Working" means the capability is implemented and, where noted in
[ROADMAP.md](ROADMAP.md), validated on real hardware. It does not imply the
project is production-ready.

| Capability | Status |
| --- | --- |
| Worker discovery and capabilities | Working |
| Versioned, platform-aware jobs | Working |
| stdout and stderr streaming | Working |
| Remote exit codes | Working |
| Current working tree transfer | Working |
| Configurable workspace exclusions | Working |
| Temporary workspace cleanup | Working |
| Firecracker/KVM isolation | Working |
| CPU, memory, and disk ceilings | Working |
| Job timeout | Working |
| cgroup resource limits | Working |
| Fork-bomb and memory-bomb containment | Working |
| Real Linux workloads (go, node, pnpm, gcc) | Working |
| Controlled job network egress | Working |
| Guest networking (LAN/host isolation) | Working |
| Artifacts | Working |
| Daemon-restart orphan cleanup | Working |

The next focus is operational hardening: authentication, health reporting, and
worker packaging. See [ROADMAP.md](ROADMAP.md) for the full plan and acceptance
criteria.

## Repository layout

```text
cmd/yonk/             client CLI
cmd/yonkd/            worker daemon
cmd/yonk-guest/       static guest agent (init)
internal/client/      worker protocol client
internal/job/         portable job and event models
internal/worker/      HTTP server and job lifecycle
internal/executor/    executor boundary, restricted and microVM executors
internal/workspace/   workspace packaging and safe extraction
internal/firecracker/ microVM API, config, and disk images
internal/guest/       guest-side agent logic
internal/guestproto/  host-guest control protocol
internal/eventstream/ shared NDJSON event sink
scripts/              worker setup
```

## Development

Run the build and checks (the same set CI runs):

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

Most executor tests run against a fake Firecracker and need neither root nor
KVM.

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for build,
test, and pull-request guidance.

## Security

Report vulnerabilities privately; do not open a public issue. See
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE).
