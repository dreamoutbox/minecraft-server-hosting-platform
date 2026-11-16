#!/usr/bin/env bash
set -euo pipefail

# ensure_shared_mount ensures /var/lib/mc-platform exists and is a shared mountpoint
# so Docker can use :rshared propagation for loop-mounted volumes.
TARGET="/var/lib/mc-platform"

# get_shared_peer_group prints the Linux mount peer group (e.g. shared:348) for a mountpoint.
#
# Arguments:
#   $1: Target mountpoint path.
#
# Example usage:
#   get_shared_peer_group /var/lib/mc-platform
get_shared_peer_group() {
	awk -v target="$1" '$5 == target { for (i=7; i<=NF; i++) if ($i ~ /^shared:/) { print $i; exit } }' /proc/self/mountinfo
}

# refresh_wsl_docker_desktop_bind unmounts a stale Docker Desktop cached bind mount
# if its shared peer group does not match /var/lib/mc-platform.
#
# Arguments:
#   None.
#
# Example usage:
#   refresh_wsl_docker_desktop_bind
refresh_wsl_docker_desktop_bind() {
	if [ -z "${WSL_DISTRO_NAME:-}" ] || [ ! -d "/mnt/wsl/docker-desktop-bind-mounts/${WSL_DISTRO_NAME}" ]; then
		return
	fi

	local target_peer
	target_peer="$(get_shared_peer_group "$TARGET")"
	if [ -z "$target_peer" ]; then
		return
	fi

	local path_hash docker_bind bind_peer
	path_hash="$(printf '%s' "$TARGET" | sha256sum | awk '{print $1}')"
	docker_bind="/mnt/wsl/docker-desktop-bind-mounts/${WSL_DISTRO_NAME}/${path_hash}"
	bind_peer="$(get_shared_peer_group "$docker_bind")"

	if [ -n "$bind_peer" ] && [ "$bind_peer" != "$target_peer" ]; then
		echo "Refreshing stale Docker Desktop bind mount $docker_bind ($bind_peer != $target_peer)..."
		if ! sudo umount -R "$docker_bind"; then
			echo "Warning: unable to unmount $docker_bind while in use; run ./dev-docker-compose-down.sh or restart Docker Desktop." >&2
		fi
	fi
}

if ! findmnt -n -o PROPAGATION "$TARGET" 2>/dev/null | grep -q "shared"; then
	echo "Setting up shared mount at $TARGET..."
	sudo mkdir -p "$TARGET"
	if ! mountpoint -q "$TARGET"; then
		sudo mount --bind "$TARGET" "$TARGET"
	fi
	sudo mount --make-rshared "$TARGET"
fi

refresh_wsl_docker_desktop_bind
