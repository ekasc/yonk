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

for tool in curl tar go; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: missing required tool: $tool (apt install curl tar golang)" >&2
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
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-x86_64.tgz" -o firecracker.tgz
tar -xzf firecracker.tgz
mv "release-v${FC_VERSION}-x86_64/firecracker-v${FC_VERSION}-x86_64" firecracker
chmod +x firecracker
rm -rf "release-v${FC_VERSION}-x86_64" firecracker.tgz

echo "==> downloading guest kernel"
curl -fsSL "$KERNEL_URL" -o vmlinux.bin

"$PREFIX/firecracker" --version
echo
echo "worker assets installed in $PREFIX"
echo "next: sudo ./scripts/build-rootfs.sh to build the toolchain rootfs"
