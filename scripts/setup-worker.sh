#!/usr/bin/env bash
# Installs the assets the microvm executor needs on a Debian worker:
# the Firecracker binary, a guest kernel, a static busybox, and a freshly
# built yonk-guest agent. Run from the repository root as root.
#
#   sudo ./scripts/setup-worker.sh
#
# Requires: curl, tar, go, and mkfs.ext4 (e2fsprogs).
set -euo pipefail

FC_VERSION="${FC_VERSION:-1.16.1}"
PREFIX="${PREFIX:-/opt/yonk}"
KERNEL_URL="${KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin}"
BUSYBOX_URL="${BUSYBOX_URL:-https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox}"

for tool in curl tar go mkfs.ext4; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: missing required tool: $tool (apt install curl tar golang e2fsprogs)" >&2
    exit 1
  }
done

if [ ! -e /dev/kvm ]; then
  echo "warning: /dev/kvm not found; microVM execution will not work" >&2
fi

mkdir -p "$PREFIX"
cd "$PREFIX"

echo "==> downloading firecracker v${FC_VERSION}"
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/v${FC_VERSION}/firecracker-v${FC_VERSION}-x86_64.tgz" -o firecracker.tgz
tar -xzf firecracker.tgz
mv "release-v${FC_VERSION}-x86_64/firecracker-v${FC_VERSION}-x86_64" firecracker
chmod +x firecracker
rm -rf "release-v${FC_VERSION}-x86_64" firecracker.tgz

echo "==> downloading guest kernel"
curl -fsSL "$KERNEL_URL" -o vmlinux.bin

echo "==> downloading busybox"
curl -fsSL "$BUSYBOX_URL" -o busybox
chmod +x busybox

echo "==> building yonk-guest"
if [ ! -f go.mod ]; then
  echo "error: run this script from the repository root (go.mod not found)" >&2
  exit 1
fi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$PREFIX/yonk-guest" ./cmd/yonk-guest

"$PREFIX/firecracker" --version
echo
echo "worker assets installed in $PREFIX"
echo "start the daemon with:"
echo "  sudo yonkd --executor microvm --listen <worker-ip>:9665 \\"
echo "    --firecracker-bin $PREFIX/firecracker --kernel $PREFIX/vmlinux.bin \\"
echo "    --guest-agent $PREFIX/yonk-guest --busybox $PREFIX/busybox"
