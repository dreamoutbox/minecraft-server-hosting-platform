#!/usr/bin/env bash
set -euo pipefail

# ensure_shared_mount ensures /var/lib/mc-platform exists and is a shared mountpoint
# so Docker can use :rshared propagation for loop-mounted volumes.
TARGET="/var/lib/mc-platform"

if findmnt -n -o PROPAGATION "$TARGET" 2>/dev/null | grep -q "shared"; then
	exit 0
fi

echo "Setting up shared mount at $TARGET..."
sudo mkdir -p "$TARGET"
if ! mountpoint -q "$TARGET"; then
	sudo mount --bind "$TARGET" "$TARGET"
fi
sudo mount --make-rshared "$TARGET"
