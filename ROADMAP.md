# Yonk roadmap

Yonk is being built in small vertical slices. Each phase must work end to end before the next one expands the trust boundary or adds more automation.

## Guiding constraints

- A submitted workload is malicious by default.
- General commands never run directly on the worker host.
- Jobs describe requirements, not executor or transport implementations.
- Tailscale is a network path, not a Yonk dependency.
- The user selects the worker until remote execution is reliable enough to justify scheduling.
- Existing operating-system and hypervisor primitives take priority over custom infrastructure.

## Phase 1: remote job protocol

Status: complete

This phase established the client, worker, portable job model, event stream, cancellation path, and executor boundary.

Delivered:

- `yonk` and `yonkd` binaries
- endpoint-based worker connections
- worker identity and capability reporting
- versioned jobs with explicit OS and architecture
- streamed stdout, stderr, status, failure, and completion events
- remote exit-code propagation
- timeout and cancellation plumbing
- structured worker logs and basic execution telemetry
- a restricted `echo` executor for protocol development

## Phase 2: workspace transfer

Status: complete

This phase made the local working tree available to the worker without requiring Git operations.

Delivered:

- gzip-compressed tar packaging
- uncommitted and untracked file transfer
- default and command-line exclusions
- multipart job and workspace uploads
- bounded archive extraction
- path traversal and unsafe-link protection
- rejection of special files and duplicate archive paths
- temporary per-job directories
- cleanup before completion is reported
- transfer and workspace telemetry in worker logs

## Phase 3: Firecracker executor

Status: complete

Jobs now run inside an ephemeral Firecracker microVM on Linux amd64, validated on a Debian 13 / KVM worker over Tailscale.

Delivered and verified on real hardware:

- Firecracker configuration generation, cpio initramfs, per-job ext4 workspace disk
- guest-initiated virtio-vsock control channel
- static `yonk-guest` init agent: mounts, workspace mount, execution, event streaming
- stdout, stderr, and exit codes stream from inside the guest
- uncommitted workspace files reach the guest (`/bin/cat marker.txt`)
- files written inside the guest never persist on the host
- provider-side vCPU and memory ceilings
- guest-side timeout: `sleep 60` with a 30-second job is killed and reported as `exit: 124`
- VM and job-state cleanup on success, failure, cancellation, timeout, and crash (zero leftover dirs or Firecracker processes after every run)
- `--executor microvm` preflight; `auto` falls back to the restricted host executor when KVM or assets are missing
- fake-Firecracker lifecycle tests run on any host

Two real-hardware defects were found and fixed during validation: `mkfs.ext4 -d` requires a pre-sized image file, and the guest's concurrent event/result writes needed a shared lock.

Remaining for later phases (not blockers): serial console capture, jailer, cgroup limits, and daemon-restart orphan cleanup.

Acceptance criteria: all met.

- `yonk run debian -- /bin/echo hello` runs inside the guest
- the Firecracker process, not `yonkd`, owns the workload process
- a file created by the job cannot appear on the host outside job state
- every successful, failed, cancelled, and timed-out job removes its VM state
- arbitrary commands remain disabled if KVM or Firecracker setup is invalid

Note: daemon-restart orphan cleanup is still tracked under phase 6 hardening; the executor removes job state on all exit paths it controls.

## Phase 4: provider-enforced limits

Status: complete

Resource requests are now enforceable ceilings controlled by the worker, validated against hostile workloads on the Debian 13 amd64 / KVM worker over Tailscale.

Delivered and verified on real hardware:

- per-job cgroup v2 subtree around every Firecracker process (`internal/executor/cgroup_linux.go`)
- `cpu.max` at the requested vCPU count, `memory.max` at requested RAM plus headroom, `pids.max` for the VM's own threads
- guest-side pids cap (256) so a fork bomb exhausts forks instead of guest kernel memory, starving the agent
- guest reaps and bounds orphaned background children before reporting completion
- CLI resource flags: `--cpu`, `--memory-mb`, `--disk-mb`, `--timeout`
- Firecracker boot switched from `--config-file` to the API socket, which also enables serial console capture (`internal/firecracker/api.go`); guest diagnostics are additionally streamed to the host over vsock
- API-driven boot also exposes the Firecracker API for future lifecycle control

Hostile workload results on real hardware (2 vCPU jobs, Debian 13):

- infinite loop: terminated at the timeout, exit 124, cgroup removed
- fork bomb: contained at 256 guest processes; host load stayed below 0.6 and SSH round-trips stayed under 0.4 s; guest survived and reported completion
- memory bomb (exponential string growth): `sh: out of memory`, exit 1, guest and host unaffected
- disk exhaustion (`dd` into `/workspace`): ENOSPC at the workspace image boundary, host unaffected
- clamp check: `--cpu 99 --memory-mb 99999` was clamped to the worker's configured 4 vCPU / 4096 MiB in both the VM config and the cgroup
- zero VM dirs, Firecracker processes, and cgroups left after every test

Acceptance criteria: all met.

- an infinite loop stops at the configured timeout
- memory exhaustion kills only the job
- a fork bomb does not materially affect the worker host
- disk exhaustion stays inside the job disk
- Debian remains reachable through SSH and Tailscale during each test
- no submitted value can raise a worker's configured maximum

## Phase 5: real Linux workloads

Status: planned

Run useful build and test commands against the transferred workspace.

Planned work:

- publish a repeatable worker image build
- decide how toolchains enter the image without putting image details in jobs
- support commands such as `go test ./...`, `pnpm test`, and project builds
- return wall time, CPU time, peak memory, transfer bytes, exit code, and termination reason
- verify stdout and stderr ordering is useful under sustained output
- document worker image compatibility and update procedures

Acceptance criteria:

- a real project with uncommitted changes runs successfully on Debian amd64
- the Mac stays mostly idle while Debian performs the work
- the local process exits with the remote command's code
- telemetry is present for successful and failed commands

Reaching these criteria completes the first useful Yonk release.

## Phase 6: artifacts and operational hardening

Status: planned

Add the pieces needed for repeated use by developers and CI systems.

Planned work:

- artifact path declarations in portable jobs
- safe artifact collection from the guest
- bounded artifact downloads with traversal protection
- daemon startup checks and health reporting
- graceful shutdown and orphan cleanup
- configurable worker-side policy for resource ceilings and concurrency
- authenticated client access independent of the underlying network
- protocol compatibility tests across released client and daemon versions
- installation packages and service definitions for Linux workers

## Later work

These items matter, but they follow reliable isolated execution:

- incremental workspace synchronization
- warm image or VM startup optimization
- automatic worker selection
- direct QUIC transport
- LAN discovery and peer discovery
- NAT traversal and relays
- public peers and reputation
- pricing and payments
- confidential executors using SEV-SNP, TDX, or Arm CCA
- additional operating systems, architectures, and accelerators

None of this should weaken the portable job model or bypass worker-controlled isolation.

## Explicitly out of scope for the first release

- marketplace and payments
- public peer discovery
- automatic scheduling
- confidential computing and remote attestation
- GPU scheduling
- distributed cache
- GUI or web dashboard
- accounts and a multi-tenant control plane
- Kubernetes
