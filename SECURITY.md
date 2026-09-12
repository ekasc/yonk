# Security policy

Yonk executes submitted workloads. It is built on the assumption that a
submitted workload is malicious, and it is being developed in the open so that
its isolation can be scrutinized.

## Project status

Yonk is **experimental (pre-alpha)**. There are no supported releases yet, and
the daemon currently has **no application-level authentication** and serves
plain HTTP. Do not expose `yonkd` to untrusted clients or the public internet.
Run it only on a trusted network path (for example, a Tailscale interface) or
behind your own access controls.

Because of this, the most common issues are expected and are not treated as
vulnerabilities:

- reaching `yonkd` from an untrusted network because it was bound to a public
  or broadly reachable address
- running the `restricted-host-process` fallback executor, which is not a
  security boundary
- resource exhaustion by a client you have already chosen to trust

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**. If that is unavailable,
contact the maintainer directly through the account that owns the repository.

Please include as much of the following as you can:

- a description of the issue and its impact
- the affected component (client, worker, executor, guest agent, workspace
  extraction, or job networking)
- a minimal reproduction, including the job or request used
- the commit or version you tested
- any suggested fix or mitigation

## Scope

In scope:

- escaping a job's microVM or otherwise running code on the worker host
- breaking the workspace extraction guarantees (path traversal, symlink
  escape, writing outside the job directory)
- breaking the job network rules (reaching the worker host, the provider LAN,
  or the tailnet from inside a job)
- unauthorized control of a running job
- memory-safety or resource-limits bypass in the daemon, executor, or guest
  agent

Out of scope:

- anything that relies on exposing the unauthenticated daemon to an untrusted
  network (see above)
- protecting a workload from the worker that runs it; that is a separate
  problem and is not part of the current design
- denial of service that requires an already-trusted client

## Response

This is a volunteer, pre-alpha project, so responses are best effort. There is
no bug bounty. Fixes are developed in the open unless disclosure would put
users at risk, in which case the report is handled privately until a fix is
available.
