#!/usr/bin/env bash
#
# dev-reset-s3.sh
# Resets the dev SeaweedFS S3 bucket by deleting all objects and recreating an empty bucket.
#
# Arguments:
#   $1: Optional S3 bucket name (defaults to $S3_BUCKET or "minecraft-backups").
#
# Example usage:
#   ./dev-reset-s3.sh
#   ./dev-reset-s3.sh minecraft-backups

set -euo pipefail

BUCKET="${1:-${S3_BUCKET:-minecraft-backups}}"
CONTAINER_NAME="mc_seaweedfs"

run_weed_shell() {
	docker exec -i "$CONTAINER_NAME" weed shell -master=localhost:9333 -filer=localhost:8888
}

if ! docker ps -q --filter "name=^/${CONTAINER_NAME}$" | grep -q .; then
	echo "Container ${CONTAINER_NAME} is not running, nothing to reset."
	exit 0
fi

echo "Resetting SeaweedFS S3 bucket '${BUCKET}' in container ${CONTAINER_NAME}..."
if printf 's3.bucket.list\n' | run_weed_shell | awk '{print $1}' | grep -Fxq "$BUCKET"; then
	printf 's3.bucket.delete -name %s\n' "$BUCKET" | run_weed_shell
fi

printf 's3.bucket.create -name %s\n' "$BUCKET" | run_weed_shell
echo "SeaweedFS S3 bucket '${BUCKET}' reset complete."
