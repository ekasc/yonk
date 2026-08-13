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

Status: next

Replace the restricted host executor with an ephemeral Firecracker microVM on Linux amd64.

Planned work:

- define the Firecracker executor configuration outside the portable job model
- prepare a minimal Linux kernel and root filesystem
- verify `/dev/kvm` and Firecracker compatibility at daemon startup
- create a fresh writable disk for each job
- place the uploaded workspace inside the guest
- start the guest with no network device by default
- execute the requested command through a small guest-side runner
- carry stdout, stderr, exit status, and termination details back to `yonkd`
- stop the VM and remove disks, sockets, and temporary files on every exit path
- replace `restricted-host-process` capability reporting with `microvm`

Acceptance criteria:

- `yonk run debian -- /bin/echo hello` runs inside the guest
- the Firecracker process, not `yonkd`, owns the workload process
- a file created by the job cannot appear on the host outside job state
- every successful, failed, cancelled, and timed-out job removes its VM state
- daemon restart cleanup removes abandoned job state
- arbitrary commands remain disabled if KVM or Firecracker setup is invalid

## Phase 4: provider-enforced limits

Status: planned

Resource requests become enforceable ceilings controlled by the worker.

Planned work:

- cap Firecracker vCPU and memory configuration
- cap workspace and writable-disk size
- place each Firecracker process in a cgroup v2 subtree
- set CPU, memory, process-count, and I/O limits where supported
- enforce wall-clock timeout outside the guest
- terminate the full VM process tree on cancellation or limit breach
- report whether completion, timeout, cancellation, or a resource limit ended the job
- keep guest networking disabled during hostile-workload validation

Acceptance criteria:

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
