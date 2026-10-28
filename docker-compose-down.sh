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
	"${DOCKER_COMPOSE[@]}" down "$@"
	echo "Platform services stopped."
}

main "$@"
