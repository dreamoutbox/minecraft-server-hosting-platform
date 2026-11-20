#!/usr/bin/env bash
#
# dev-deploy-vps.sh
# Wrapper around dev-docker-build-image.sh that builds all platform Docker images
# locally, exports them to a compressed archive, rsyncs the archive and runtime
# compose/script files to a Linux VPS, loads the images remotely via SSH, and
# starts the Docker Compose stack for dev testing.
#
# Environment variables:
#   APISERVER_IMAGE:  Tag for apiserver image (default: dreamoutbox/minecraft-hosting-apiserver:latest)
#   WORKER_IMAGE:     Tag for worker image (default: dreamoutbox/minecraft-hosting-worker:latest)
#   FILESERVER_IMAGE: Tag for fileserver image (default: dreamoutbox/minecraft-hosting-fileserver:latest)
#   JOBWORKER_IMAGE:  Tag for jobworker image (default: dreamoutbox/minecraft-hosting-jobworker:latest)
#   REMOTE_DIR:       Target directory on VPS (default: minecraft-server-hosting-platform)
#
# Arguments:
#   $1: Target VPS in <user>@<ip> format (required).
#   $2: Optional remote directory override (defaults to REMOTE_DIR).
#
# Example usage:
#   ./dev-deploy-vps.sh root@203.0.113.10
#   ./dev-deploy-vps.sh ubuntu@203.0.113.10 /opt/minecraft-hosting
#   REMOTE_DIR=mc-dev ./dev-deploy-vps.sh ubuntu@203.0.113.10

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARCHIVE_NAME="mc-platform-images.tar.gz"

APISERVER_IMAGE="${APISERVER_IMAGE:-dreamoutbox/minecraft-hosting-apiserver:latest}"
WORKER_IMAGE="${WORKER_IMAGE:-dreamoutbox/minecraft-hosting-worker:latest}"
FILESERVER_IMAGE="${FILESERVER_IMAGE:-dreamoutbox/minecraft-hosting-fileserver:latest}"
JOBWORKER_IMAGE="${JOBWORKER_IMAGE:-dreamoutbox/minecraft-hosting-jobworker:latest}"

# usage prints CLI usage instructions and exits with status 1.
#
# Arguments:
#   None.
#
# Example usage:
#   usage
usage() {
	echo "Usage: $0 <user>@<ip> [remote_dir]" >&2
	echo "Example: $0 root@203.0.113.10" >&2
	exit 1
}

# require_command verifies a required CLI binary is available in PATH.
#
# Arguments:
#   $1: Command name to check.
#
# Example usage:
#   require_command rsync
require_command() {
	local cmd="$1"
	if ! command -v "${cmd}" >/dev/null 2>&1; then
		echo "Error: '${cmd}' is required but not installed." >&2
		exit 1
	fi
}

# save_images exports all built platform Docker images into a gzipped tarball.
#
# Arguments:
#   $1: Destination archive file path.
#
# Example usage:
#   save_images "/tmp/mc-platform-images.tar.gz"
save_images() {
	local archive_path="$1"
	echo "Saving Docker images to ${archive_path}..."
	docker save \
		"${APISERVER_IMAGE}" \
		"${WORKER_IMAGE}" \
		"${FILESERVER_IMAGE}" \
		"${JOBWORKER_IMAGE}" | gzip > "${archive_path}"
	echo "Saved image archive ($(du -h "${archive_path}" | cut -f1))."
}

# sync_to_vps ensures the remote directory exists and rsyncs runtime files and the image archive.
#
# Arguments:
#   $1: SSH target (<user>@<ip>).
#   $2: Remote directory path on the VPS.
#   $3: Local path to the saved Docker image archive.
#
# Example usage:
#   sync_to_vps "root@203.0.113.10" "minecraft-server-hosting-platform" "/tmp/mc-platform-images.tar.gz"
sync_to_vps() {
	local target="$1"
	local remote_dir="$2"
	local archive_path="$3"

	echo "Preparing remote directory ${target}:${remote_dir}..."
	ssh "${target}" "mkdir -p '${remote_dir}'"

	echo "Syncing compose files, scripts, and Docker images to ${target}:${remote_dir}..."
	rsync -avz --progress \
		"${archive_path}" \
		"${SCRIPT_DIR}/docker-compose.base.yml" \
		"${SCRIPT_DIR}/docker-compose.app.yml" \
		"${SCRIPT_DIR}/docker-compose.stripe.yml" \
		"${SCRIPT_DIR}/docker-stripe" \
		"${SCRIPT_DIR}/template.env" \
		"${SCRIPT_DIR}"/dev-*.sh \
		"${SCRIPT_DIR}"/debug-*.sh \
		"${target}:${remote_dir}/"
}

# ssh_tty runs an ssh command, allocating a pseudo-tty when stdin is interactive.
#
# Arguments:
#   $@: Arguments forwarded to ssh.
#
# Example usage:
#   ssh_tty "root@203.0.113.10" "sudo systemctl status docker"
ssh_tty() {
	if [[ -t 0 ]]; then
		ssh -t "$@"
	else
		ssh "$@"
	fi
}

# ensure_remote_docker checks if docker is installed on the VPS and installs it via get.docker.com if missing.
#
# Arguments:
#   $1: SSH target (<user>@<ip>).
#
# Example usage:
#   ensure_remote_docker "root@203.0.113.10"
ensure_remote_docker() {
	local target="$1"

	if ssh "${target}" "command -v docker >/dev/null 2>&1"; then
		echo "Docker is already installed on ${target}."
		return
	fi

	echo "Docker not found on ${target}. Installing via https://get.docker.com..."
	ssh_tty "${target}" '
		if [ "$(id -u)" -eq 0 ]; then
			curl -fsSL https://get.docker.com | sh
		else
			curl -fsSL https://get.docker.com | sudo sh
			sudo usermod -aG docker "$USER"
		fi
		if command -v systemctl >/dev/null 2>&1; then
			if [ "$(id -u)" -eq 0 ]; then
				systemctl enable --now docker
			else
				sudo systemctl enable --now docker
			fi
		fi
	'
	echo "Docker installed successfully on ${target}."
}

# load_images_remote loads the transferred image archive on the VPS and removes the remote tarball.
#
# Arguments:
#   $1: SSH target (<user>@<ip>).
#   $2: Remote directory path on the VPS.
#
# Example usage:
#   load_images_remote "root@203.0.113.10" "minecraft-server-hosting-platform"
load_images_remote() {
	local target="$1"
	local remote_dir="$2"
	local remote_archive="${remote_dir}/${ARCHIVE_NAME}"

	echo "Loading Docker images on ${target}..."
	ssh "${target}" "docker load -i '${remote_archive}' && rm -f '${remote_archive}'"
	echo "Docker images loaded successfully on ${target}."
}

# start_remote_compose initializes .env if missing and runs dev-docker-compose-up.sh on the VPS.
#
# Arguments:
#   $1: SSH target (<user>@<ip>).
#   $2: Remote directory path on the VPS.
#
# Example usage:
#   start_remote_compose "root@203.0.113.10" "minecraft-server-hosting-platform"
start_remote_compose() {
	local target="$1"
	local remote_dir="$2"

	echo "Starting Docker Compose stack on ${target}..."
	ssh_tty "${target}" "cd '${remote_dir}' && ([ -f .env ] || cp template.env .env) && ./dev-docker-compose-up.sh && docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'"
}

# main parses arguments, builds images locally, syncs to the VPS, loads images, and starts compose.
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

	require_command docker
	require_command rsync
	require_command ssh
	require_command gzip

	local archive_path="${SCRIPT_DIR}/${ARCHIVE_NAME}"

	"${SCRIPT_DIR}/dev-docker-build-image.sh"
	save_images "${archive_path}"
	sync_to_vps "${target}" "${remote_dir}" "${archive_path}"
	ensure_remote_docker "${target}"
	load_images_remote "${target}" "${remote_dir}"
	start_remote_compose "${target}" "${remote_dir}"

	local host_ip="${target#*@}"
	echo ""
	echo "Dev deploy complete on ${target}."
	echo "  Apiserver: http://${host_ip}:8080"
	echo "  Adminer:   http://${host_ip}:8081"
	echo "  Logs:      ssh ${target} 'cd ${remote_dir} && docker compose -f docker-compose.base.yml -f docker-compose.app.yml -f docker-compose.stripe.yml logs -f'"
}

main "$@"
