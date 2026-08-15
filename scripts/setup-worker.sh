#!/usr/bin/env bash
# Installs the core assets the microvm executor needs on a Debian worker:
# the Firecracker binary, a guest kernel, and a freshly built yonk-guest
# agent. The read-only guest rootfs with toolchains is built separately:
#   sudo ./scripts/build-rootfs.sh
# Run both from the repository root as root.
#
#   sudo ./scripts/setup-worker.sh
#
# Requires: curl, tar, go.
set -euo pipefail

FC_VERSION="${FC_VERSION:-1.16.1}"
PREFIX="${PREFIX:-/opt/yonk}"
KERNEL_URL="${KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260722-38359b8055fc-0/x86_64/vmlinux-5.10.260}"
# Pinned SHA-256 of the kernel downloaded from KERNEL_URL. This hash is
# version-specific: it MUST be updated whenever KERNEL_URL changes, and the
# script fails closed if the downloaded kernel does not match.
KERNEL_SHA256="9b910588df6f8b988d2dff45279d15323c54284e3d3b99ed777fcaec7e29a572"

for tool in curl tar go sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: missing required tool: $tool (apt install curl tar golang coreutils)" >&2
    exit 1
  }
done

[ -f go.mod ] || {
  echo "error: run this script from the repository root (go.mod not found)" >&2
  exit 1
}

if [ ! -e /dev/kvm ]; then
  echo "warning: /dev/kvm not found; microVM execution will not work" >&2
fi

mkdir -p "$PREFIX"

echo "==> building yonk-guest"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$PREFIX/yonk-guest" ./cmd/yonk-guest

cd "$PREFIX"

echo "==> downloading firecracker v${FC_VERSION}"
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-x86_64.tgz" -o "firecracker-v${FC_VERSION}-x86_64.tgz"
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-x86_64.tgz.sha256.txt" -o "firecracker-v${FC_VERSION}-x86_64.tgz.sha256.txt"
# Verify the tarball before extracting; the release publishes the checksum
# file beside the tarball.
sha256sum -c "firecracker-v${FC_VERSION}-x86_64.tgz.sha256.txt" >/dev/null || {
  echo "error: firecracker tarball SHA-256 mismatch; aborting" >&2
  exit 1
}
tar -xzf "firecracker-v${FC_VERSION}-x86_64.tgz"
mv "release-v${FC_VERSION}-x86_64/firecracker-v${FC_VERSION}-x86_64" firecracker
chmod +x firecracker
rm -rf "release-v${FC_VERSION}-x86_64" "firecracker-v${FC_VERSION}-x86_64.tgz" "firecracker-v${FC_VERSION}-x86_64.tgz.sha256.txt"

echo "==> downloading guest kernel"
curl -fsSL "$KERNEL_URL" -o vmlinux.bin
# Verify the pinned hash before the kernel is used; see KERNEL_SHA256 above.
echo "$KERNEL_SHA256  vmlinux.bin" | sha256sum -c - >/dev/null || {
  echo "error: guest kernel SHA-256 mismatch; expected $KERNEL_SHA256" >&2
  exit 1
}

"$PREFIX/firecracker" --version
echo
echo "worker assets installed in $PREFIX"
echo "next: sudo ./scripts/build-rootfs.sh to build the toolchain rootfs"
