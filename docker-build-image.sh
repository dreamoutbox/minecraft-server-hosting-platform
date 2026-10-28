#!/usr/bin/env bash
#
# docker-build-image.sh
# Builds the Docker container images for the Minecraft Hosting Platform services.
# Configurable via environment variables SERVER_IMAGE and WORKER_IMAGE.
#
# Environment variables:
#   SERVER_IMAGE: Tag for the web orchestrator server image (default: minecraft-hosting-server:latest)
#   WORKER_IMAGE: Tag for the host worker daemon image (default: minecraft-hosting-worker:latest)
#
# Arguments:
#   $@: Optional flags forwarded directly to docker build (e.g., --no-cache).
#
# Example usage:
#   ./docker-build-image.sh
#   ./docker-build-image.sh --no-cache
#   SERVER_IMAGE=myregistry/server:v1.0 WORKER_IMAGE=myregistry/worker:v1.0 ./docker-build-image.sh

set -euo pipefail

# Configuration with environment variable overrides
SERVER_IMAGE="${SERVER_IMAGE:-minecraft-hosting-server:latest}"
WORKER_IMAGE="${WORKER_IMAGE:-minecraft-hosting-worker:latest}"

# build_server_image compiles and packages the orchestrator binary into an Alpine container image.
#
# Arguments:
#   $@: Extra arguments passed to docker build.
#
# Example usage:
#   build_server_image --no-cache
build_server_image() {
	echo "Building server image: ${SERVER_IMAGE}..."
	docker build -t "${SERVER_IMAGE}" -f Dockerfile.server . "$@"
	echo "Server image built successfully: ${SERVER_IMAGE}"
}

# build_worker_image compiles and packages the worker daemon binary into an Alpine container image.
#
# Arguments:
#   $@: Extra arguments passed to docker build.
#
# Example usage:
#   build_worker_image --no-cache
build_worker_image() {
	echo "Building worker image: ${WORKER_IMAGE}..."
	docker build -t "${WORKER_IMAGE}" -f Dockerfile.worker . "$@"
	echo "Worker image built successfully: ${WORKER_IMAGE}"
}

# main orchestrates building all platform images.
#
# Arguments:
#   $@: Flags forwarded to docker build.
#
# Example usage:
#   main "$@"
main() {
	echo "Starting platform Docker image builds..."
	echo "  Server tag: ${SERVER_IMAGE}"
	echo "  Worker tag: ${WORKER_IMAGE}"

	build_server_image "$@"
	build_worker_image "$@"

	echo "All platform images built successfully."
}

main "$@"
