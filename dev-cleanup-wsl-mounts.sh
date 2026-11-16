#!/usr/bin/env bash
set -euo pipefail

# dev-cleanup-wsl-mounts.sh
# Unmounts stale Docker Desktop WSL2 cached bind mounts under
# /mnt/wsl/docker-desktop-bind-mounts/<distro>/ that reference /dev/loop*
# or /var/lib/mc-platform/mounts/* so recreated server paths do not reuse
# stale loop devices.

if [ -z "${WSL_DISTRO_NAME:-}" ] || [ ! -d "/mnt/wsl/docker-desktop-bind-mounts/${WSL_DISTRO_NAME}" ]; then
	exit 0
fi

bind_root="/mnt/wsl/docker-desktop-bind-mounts/${WSL_DISTRO_NAME}/"

while IFS= read -r target; do
	[ -n "$target" ] || continue
	echo "Unmounting stale Docker Desktop bind mount $target..."
	sudo umount -d -R "$target" 2>/dev/null || sudo umount -l "$target" 2>/dev/null || true
done < <(
	awk -v root="$bind_root" '
	{
		mp = $5
		src_root = $4
		dev = ""
		for (i = 7; i <= NF; i++) {
			if ($i == "-") {
				dev = $(i + 2)
				break
			}
		}
		if (index(mp, root) == 1) {
			rel = substr(mp, length(root) + 1)
			if (index(rel, "/") == 0 && (dev ~ /^\/dev\/loop/ || index(src_root, "/var/lib/mc-platform/mounts") == 1)) {
				print mp
			}
		}
	}' /proc/self/mountinfo | sort -u
)
