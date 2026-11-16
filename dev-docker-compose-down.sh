#!/usr/bin/env bash
#
# docker-compose-down.sh
# Stops and removes platform service containers managed by docker compose.
#
# Arguments:
#   $@: Optional flags forwarded to docker compose down (e.g., -v, --remove-orphans).
#
# Example usage:
#   ./docker-compose-down.sh
#   ./docker-compose-down.sh -v
#   ./docker-compose-down.sh --remove-orphans

set -euo pipefail

# resolve_compose_command detects available docker compose executable.
#
# Arguments:
#   None.
#
# Example usage:
#   resolve_compose_command
resolve_compose_command() {
	if docker compose version >/dev/null 2>&1; then
		DOCKER_COMPOSE=(docker compose)
	elif command -v docker-compose >/dev/null 2>&1; then
		DOCKER_COMPOSE=(docker-compose)
	else
		echo "Error: docker compose or docker-compose is required but not installed." >&2
		exit 1
	fi
}

# cleanup_orphaned_servers removes lingering managed server containers and unmounts
# leftover volume mountpoints if the worker was not running during compose down.
#
# Arguments:
#   None.
#
# Example usage:
#   cleanup_orphaned_servers
cleanup_orphaned_servers() {
	local cids
	cids=$({
		docker ps -aq --filter "label=platform=minecraft-server-hosting" 2>/dev/null || true
		docker ps -aq --filter "name=^/mc-server-" 2>/dev/null || true
	} | sort -u)

	if [ -n "$cids" ]; then
		echo "Removing leftover managed server containers..."
		# shellcheck disable=SC2086
		docker rm -f $cids >/dev/null
	fi

	local mounts_dir="/var/lib/mc-platform/mounts"
	if [ -d "$mounts_dir" ]; then
		for dir in "$mounts_dir"/*; do
			[ -e "$dir" ] || continue
			if mountpoint -q "$dir"; then
				echo "Unmounting leftover volume $dir..."
				sudo umount -d "$dir"
			fi
		done
	fi
}

# main tears down docker compose services.
#
# Arguments:
#   $@: Command line arguments to forward to docker compose down.
#
# Example usage:
#   main "$@"
main() {
	resolve_compose_command

	echo "Stopping and tearing down platform services..."
	"${DOCKER_COMPOSE[@]}" -f docker-compose.yml -f docker-compose.dev.yml down "$@"
	cleanup_orphaned_servers
	echo "Platform services stopped."
}

main "$@"
