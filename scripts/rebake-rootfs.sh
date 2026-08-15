#!/usr/bin/env bash
# Bakes the read-only guest rootfs image from a staging directory without a
# full debootstrap, so toolchain updates only re-run mkfs.
#
#   sudo ./scripts/rebake-rootfs.sh /path/to/staging
set -euo pipefail

STAGING="${1:?usage: rebake-rootfs.sh <staging-directory>}"
PREFIX="${PREFIX:-/opt/yonk}"
IMAGE="$PREFIX/rootfs.ext4"

[ -d "$STAGING" ] || {
  echo "error: $STAGING is not a directory" >&2
  exit 1
}

size_mb=$(du -sm --exclude=proc --exclude=sys --exclude=dev "$STAGING" | cut -f1)
image_mb=$((size_mb * 115 / 100 + 64))
new_image="$IMAGE.new.$$"
truncate -s "${image_mb}M" "$new_image"
mkfs.ext4 -q -F -m 0 -d "$STAGING" "$new_image"
mv -f "$new_image" "$IMAGE"

echo "rootfs baked: $IMAGE ($(ls -lh "$IMAGE" | awk '{print $5}'))"
