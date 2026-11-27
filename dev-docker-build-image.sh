#!/usr/bin/env bash
#
# docker-build-image.sh
# Builds the Docker container images for the Minecraft Hosting Platform services.
# Configurable via environment variables APISERVER_IMAGE, WORKER_IMAGE, and FILESERVER_IMAGE.
#
# Environment variables:
#   APISERVER_IMAGE: Tag for the web orchestrator apiserver image (default: dreamoutbox/minecraft-hosting-apiserver:latest)
#   WORKER_IMAGE: Tag for the host worker daemon image (default: dreamoutbox/minecraft-hosting-worker:latest)
#   FILESERVER_IMAGE: Tag for the node fileserver image (default: dreamoutbox/minecraft-hosting-fileserver:latest)
#
# Arguments:
#   $@: Optional flags forwarded directly to docker build (e.g., --no-cache).
#
# Example usage:
#   ./docker-build-image.sh
#   ./docker-build-image.sh --no-cache
#   APISERVER_IMAGE=myregistry/apiserver:v1.0 WORKER_IMAGE=myregistry/worker:v1.0 ./docker-build-image.sh

set -euo pipefail

# Configuration with environment variable overrides
APISERVER_IMAGE="${APISERVER_IMAGE:-dreamoutbox/minecraft-hosting-apiserver:latest}"
WORKER_IMAGE="${WORKER_IMAGE:-dreamoutbox/minecraft-hosting-worker:latest}"
FILESERVER_IMAGE="${FILESERVER_IMAGE:-dreamoutbox/minecraft-hosting-fileserver:latest}"
JOBWORKER_IMAGE="${JOBWORKER_IMAGE:-dreamoutbox/minecraft-hosting-jobworker:latest}"

# build_apiserver_image compiles and packages the orchestrator binary into an Alpine container image.
#
# Arguments:
#   $@: Extra arguments passed to docker build.
#
# Example usage:
#   build_apiserver_image --no-cache
build_apiserver_image() {
	echo "Building apiserver image: ${APISERVER_IMAGE}..."
	docker build -t "${APISERVER_IMAGE}" -f Dockerfile.apiserver . "$@"
	echo "API server image built successfully: ${APISERVER_IMAGE}"
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
	docker build -t "${WORKER_IMAGE}" -f Dockerfile.nodemanager . "$@"
	echo "Worker image built successfully: ${WORKER_IMAGE}"
}

# build_fileserver_image compiles and packages the node fileserver binary into an Alpine container image.
#
# Arguments:
#   $@: Extra arguments passed to docker build.
#
# Example usage:
#   build_fileserver_image --no-cache
build_fileserver_image() {
	echo "Building fileserver image: ${FILESERVER_IMAGE}..."
	docker build -t "${FILESERVER_IMAGE}" -f fileserver/Dockerfile . "$@"
	echo "Fileserver image built successfully: ${FILESERVER_IMAGE}"
}

# build_jobworker_image compiles and packages the background jobworker binary into an Alpine container image.
#
# Arguments:
#   $@: Extra arguments passed to docker build.
#
# Example usage:
#   build_jobworker_image --no-cache
build_jobworker_image() {
	echo "Building jobworker image: ${JOBWORKER_IMAGE}..."
	docker build -t "${JOBWORKER_IMAGE}" -f Dockerfile.jobworker . "$@"
	echo "Jobworker image built successfully: ${JOBWORKER_IMAGE}"
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
	echo "  API server tag: ${APISERVER_IMAGE}"
	echo "  Worker tag: ${WORKER_IMAGE}"
	echo "  Fileserver tag: ${FILESERVER_IMAGE}"
	echo "  Jobworker tag: ${JOBWORKER_IMAGE}"

	build_apiserver_image "$@"
	build_worker_image "$@"
	build_fileserver_image "$@"
	build_jobworker_image "$@"

	echo "All platform images built successfully."
}

main "$@"
