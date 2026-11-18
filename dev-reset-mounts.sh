#!/usr/bin/env bash
set -euo pipefail

MOUNTS_DIR="/var/lib/mc-platform/mounts"
IMAGES_DIR="/var/lib/mc-platform/images"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -d "$MOUNTS_DIR" ]; then
	# Unmount any active mountpoints under /var/lib/mc-platform/mounts/ and detach loop devices.
	for dir in "$MOUNTS_DIR"/*; do
		[ -e "$dir" ] || continue
		if mountpoint -q "$dir"; then
			echo "Unmounting $dir..."
			sudo umount -d "$dir"
		fi
	done

	echo "Removing contents of $MOUNTS_DIR..."
	sudo rm -rf "${MOUNTS_DIR:?}"/*
else
	echo "Mounts directory $MOUNTS_DIR does not exist, nothing to clean."
fi

"$SCRIPT_DIR/dev-cleanup-wsl-mounts.sh"

# Detach any lingering loop devices referencing platform volume images (including deleted files).
while IFS= read -r line; do
	dev="${line%%:*}"
	if [ -n "$dev" ]; then
		echo "Detaching loop device $dev..."
		sudo losetup -d "$dev" || true
	fi
done < <(losetup -a 2>/dev/null | grep -E '\((/var/lib/mc-platform)?/images/' || true)

if [ -d "$IMAGES_DIR" ]; then
	echo "Removing contents of $IMAGES_DIR..."
	sudo rm -rf "${IMAGES_DIR:?}"/*
fi

echo "Mounts cleanup complete."
