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
| Firecracker/KVM isolation | Working |
| CPU and memory ceilings | Working |
| Job timeout | Working |
| cgroup resource limits | Working |
| Fork-bomb and memory-bomb containment | Working |
| Real Linux workloads (go, node, pnpm, gcc) | Working |
| Controlled job network egress | Working |
| Guest networking (LAN/host isolation) | Working |
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

## Worker setup

On the Debian worker, install the core assets and build the toolchain rootfs (run from the repository root as root):

```bash
sudo ./scripts/setup-worker.sh    # Firecracker, kernel, yonk-guest agent
sudo ./scripts/build-rootfs.sh    # read-only rootfs with Go, Node, pnpm, gcc, git, make
```

The setup script downloads Firecracker and a guest kernel into `/opt/yonk` and builds the static `yonk-guest` agent. The rootfs script debootstraps a minimal Debian, installs the toolchains, bakes the agent in, and writes `/opt/yonk/rootfs.ext4` (immutable and shared across jobs). It requires `curl`, `tar`, `go`, `debootstrap`, `e2fsprogs`, and `/dev/kvm`. After toolchain changes, `sudo ./scripts/rebake-rootfs.sh <staging>` refreshes the image without re-running debootstrap.

Start `yonkd` with the microVM executor:

```bash
sudo yonkd --executor microvm \
  --listen 100.x.y.z:9665 \
  --firecracker-bin /opt/yonk/firecracker \
  --kernel /opt/yonk/vmlinux.bin \
  --rootfs /opt/yonk/rootfs.ext4
```

The default is `--executor auto`, which uses the microVM executor when KVM and all assets are available and falls back to the restricted host executor otherwise. `--executor microvm` fails loudly instead of falling back.

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

Resource requests and the timeout can be set per job; the worker enforces them as ceilings:

```bash
./yonk run debian --cpu 4 --memory-mb 4096 --disk-mb 8192 --timeout 300 -- go test ./...
```

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

## Job networking

Jobs have **no network access by default**. For workloads that need to reach the internet (installs, module downloads):

```bash
./yonk run debian --network egress -- pnpm install
./yonk run debian --network egress -- go mod download
```

Egress is controlled and minimal: jobs reach public destinations only. Host-side nftables rules drop all inbound traffic from job taps (jobs cannot reach the worker's SSH, yonkd, or other services) and drop private, CGNAT (Tailscale), link-local, and reserved destinations (jobs cannot reach the provider LAN or other tailnet machines). IPv6 is disabled, each job gets an isolated /30 and TAP, and the worker rate-limits per-job bandwidth and packets (`--max-egress-mbps`, `--max-egress-pps`). The worker also provisions the guest's resolver (`--guest-resolver`).

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

The Firecracker executor puts every job in a fresh microVM with no network device. The worker controls vCPU, memory, disk size, and runtime from outside the guest, then stops the VM and removes all job state after every run.

The `restricted-host-process` executor remains only as a no-KVM fallback and still permits just `echo`; general commands never run directly on the host.

Protecting a workload from a worker is a separate problem. Confidential computing and remote attestation belong later in the roadmap and are not part of the initial system.

## Repository layout

```text
cmd/yonk/           client CLI
cmd/yonkd/          worker daemon
cmd/yonk-guest/     static guest agent (initramfs init)
internal/client/    worker protocol client
internal/job/       portable job and event models
internal/worker/    HTTP server and job lifecycle
internal/executor/  executor boundary, restricted and microVM executors
internal/workspace  workspace packaging and safe extraction
internal/firecracker  microVM config, initramfs, and disk images
internal/guest/     guest-side agent logic
internal/guestproto host-guest control protocol
internal/eventstream shared NDJSON event sink
scripts/            worker setup
```

## Roadmap

The immediate priority is completing Firecracker validation on the Debian worker. General commands stay disabled until jobs run inside a microVM with host-enforced limits.

See [ROADMAP.md](ROADMAP.md) for the full plan and acceptance criteria.

## Development

Run the test suite and static checks:

```bash
go test -race ./...
go vet ./...
```
