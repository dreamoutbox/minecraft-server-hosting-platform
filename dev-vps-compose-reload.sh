#!/usr/bin/env bash
#
# dev-vps-compose-reload.sh
# Rsyncs updated Docker Compose files, helper scripts, and ./dev-vps.env (as .env)
# to a Linux VPS and runs docker compose up -d remotely.
#
# Environment variables:
#   REMOTE_DIR: Target directory on VPS (default: minecraft-server-hosting-platform)
#
# Arguments:
#   $1: Target VPS in <user>@<ip> format (required).
#   $2: Optional remote directory override (defaults to REMOTE_DIR).
#
# Example usage:
#   ./dev-vps-compose-reload.sh root@123.456.789.123
#   ./dev-vps-compose-reload.sh ubuntu@123.456.789.123 /opt/minecraft-hosting

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# usage prints CLI usage instructions and exits with status 1.
#
# Arguments:
#   None.
#
# Example usage:
#   usage
usage() {
	echo "Usage: $0 <user>@<ip> [remote_dir]" >&2
	echo "Example: $0 root@123.456.789.123" >&2
	exit 1
}

# ssh_tty runs an ssh command, allocating a pseudo-tty when stdin is interactive.
#
# Arguments:
#   $@: Arguments forwarded to ssh.
#
# Example usage:
#   ssh_tty "root@123.456.789.123" "cd minecraft-server-hosting-platform && ./dev-docker-compose-up.sh"
ssh_tty() {
	if [[ -t 0 ]]; then
		ssh -t "$@"
	else
		ssh "$@"
	fi
}

# sync_compose_and_env uploads compose files, helper scripts, and dev-vps.env (as .env) to the VPS.
#
# Arguments:
#   $1: SSH target (<user>@<ip>).
#   $2: Remote directory path on the VPS.
#
# Example usage:
#   sync_compose_and_env "root@123.456.789.123" "minecraft-server-hosting-platform"
sync_compose_and_env() {
	local target="$1"
	local remote_dir="$2"
	local env_file="${SCRIPT_DIR}/dev-vps.env"

	if [[ ! -f "${env_file}" ]]; then
		echo "Error: ${env_file} not found." >&2
		exit 1
	fi

	ssh "${target}" "mkdir -p '${remote_dir}'"

	echo "Syncing compose files and scripts to ${target}:${remote_dir}..."
	rsync -avz --progress \
		"${SCRIPT_DIR}/docker-compose.base.yml" \
		"${SCRIPT_DIR}/docker-compose.app.yml" \
		"${SCRIPT_DIR}/docker-compose.stripe.yml" \
		"${SCRIPT_DIR}/docker-stripe" \
		"${SCRIPT_DIR}"/dev-*.sh \
		"${SCRIPT_DIR}"/debug-*.sh \
		"${target}:${remote_dir}/"

	echo "Uploading ${env_file} to ${target}:${remote_dir}/.env..."
	rsync -avz --progress "${env_file}" "${target}:${remote_dir}/.env"
}

# main parses arguments, syncs compose/env files to the VPS, and runs compose up.
#
# Arguments:
#   $1: Target VPS (<user>@<ip>).
#   $2: Optional remote directory override.
#
# Example usage:
#   main "$@"
main() {
	if [[ $# -lt 1 || $# -gt 2 ]]; then
		usage
	fi

	local target="$1"
	local remote_dir="${2:-${REMOTE_DIR:-minecraft-server-hosting-platform}}"
	# Strip leading ~/ so single-quoted remote paths resolve cleanly relative to $HOME.
	remote_dir="${remote_dir#\~/}"

	if [[ "${target}" != *@* ]]; then
		echo "Error: target must be in <user>@<ip> format (got '${target}')." >&2
		usage
	fi

	sync_compose_and_env "${target}" "${remote_dir}"

	echo "Reloading Docker Compose stack on ${target}..."
	ssh_tty "${target}" "cd '${remote_dir}' && ./dev-docker-compose-up.sh && docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'"
}

main "$@"
