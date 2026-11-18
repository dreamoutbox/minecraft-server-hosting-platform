#!/usr/bin/env bash
#
# docker-compose-up.sh
# Starts the platform service containers using docker compose.
#
# Arguments:
#   $@: Optional flags or service names forwarded to docker compose up.
#       Runs in detached mode (-d) by default.
#
# Example usage:
#   ./docker-compose-up.sh
#   ./docker-compose-up.sh --build
#   ./docker-compose-up.sh postgres seaweedfs

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

# main starts docker compose services.
#
# Arguments:
#   $@: Command line arguments to forward to docker compose up.
#
# Example usage:
#   main "$@"
main() {
	local script_dir
	script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

	resolve_compose_command
	"$script_dir/dev-setup-shared-mount.sh"

	echo "Starting platform services with docker compose..."
	"${DOCKER_COMPOSE[@]}" -f docker-compose.base.yml -f docker-compose.app.yml up -d "$@"
	echo "Platform services started successfully."
}

main "$@"
