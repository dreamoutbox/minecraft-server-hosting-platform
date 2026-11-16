#!/usr/bin/env bash
set -euo pipefail

MOUNTS_DIR="/var/lib/mc-platform/mounts"

if [ ! -d "$MOUNTS_DIR" ]; then
	echo "Mounts directory $MOUNTS_DIR does not exist, nothing to clean."
	exit 0
fi

# Unmount any active mountpoints under /var/lib/mc-platform/mounts/
for dir in "$MOUNTS_DIR"/*; do
	[ -e "$dir" ] || continue
	if mountpoint -q "$dir"; then
		echo "Unmounting $dir..."
		sudo umount "$dir"
	fi
done

echo "Removing contents of $MOUNTS_DIR..."
sudo rm -rf "${MOUNTS_DIR:?}"/*
echo "Mounts cleanup complete."
