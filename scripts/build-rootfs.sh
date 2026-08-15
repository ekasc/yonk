#!/usr/bin/env bash
# Builds the read-only guest rootfs that carries the toolchains and the
# yonk-guest agent for real workloads. Run as root on the worker after
# scripts/setup-worker.sh has installed the guest agent.
#
#   sudo ./scripts/build-rootfs.sh
#
# Requires: debootstrap, mkfs.ext4, network access to the Debian mirrors.
set -euo pipefail

PREFIX="${PREFIX:-/opt/yonk}"
RELEASE="${RELEASE:-trixie}"
MIRROR="${MIRROR:-http://deb.debian.org/debian}"
GUEST_BIN="${GUEST_BIN:-$PREFIX/yonk-guest}"
IMAGE="$PREFIX/rootfs.ext4"

STAGING="$(mktemp -d /tmp/yonk-rootfs.XXXXXX)"
trap 'umount -R "$STAGING/dev" "$STAGING/sys" "$STAGING/proc" 2>/dev/null || true; rm -rf "$STAGING"' EXIT

for tool in debootstrap mkfs.ext4; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: missing required tool: $tool (apt install debootstrap e2fsprogs)" >&2
    exit 1
  }
done
[ -x "$GUEST_BIN" ] || {
  echo "error: $GUEST_BIN not found; run scripts/setup-worker.sh first" >&2
  exit 1
}

echo "==> debootstrap $RELEASE (minbase)"
debootstrap --variant=minbase "$RELEASE" "$STAGING" "$MIRROR"

# apt inside the chroot needs DNS, /proc, /sys, and /dev.
cp -f /etc/resolv.conf "$STAGING/etc/resolv.conf"
mount -t proc proc "$STAGING/proc"
mount -t sysfs sysfs "$STAGING/sys"
if ! mount -t devtmpfs devtmpfs "$STAGING/dev"; then
  mount --bind /dev "$STAGING/dev"
fi

echo "==> installing toolchains"
chroot "$STAGING" apt-get update
chroot "$STAGING" apt-get install -y --no-install-recommends \
  build-essential \
  ca-certificates \
  curl \
  git \
  golang \
  make \
  nodejs \
  npm \
  procps

# Debian trixie does not ship a pnpm package; install it via npm and pin a
# version compatible with the Debian Node.js release.
chroot "$STAGING" npm install -g pnpm@9 >/dev/null

echo "==> installing yonk-guest"
install -m 0755 "$GUEST_BIN" "$STAGING/usr/sbin/yonk-guest"
mkdir -p "$STAGING/workspace" # the read-only rootfs mounts the writable job disk here

# The chroot mounts must go before the image is baked; mkfs.ext4 -d walks
# the directory and would copy the mounted pseudo-filesystems otherwise.
umount "$STAGING/proc" "$STAGING/sys" "$STAGING/dev" 2>/dev/null || true

echo "==> baking $IMAGE"
rm -f "$IMAGE"
size_mb=$(du -sm --exclude=proc --exclude=sys --exclude=dev "$STAGING" | cut -f1)
image_mb=$((size_mb * 115 / 100 + 64))
truncate -s "${image_mb}M" "$IMAGE"
mkfs.ext4 -q -F -m 0 -d "$STAGING" "$IMAGE"
ls -lh "$IMAGE"echo
echo "worker rootfs ready: $IMAGE"
echo "start the daemon with: yonkd --executor microvm --rootfs $IMAGE"
