#!/usr/bin/env bash
#
# backup-ext4-img-to-s3.sh
# Triggers an immediate ext4 .img backup to S3 via apiserver (which enqueues backup_image job(s);
# jobworker mints presigned S3 PUT URLs and invokes cmd/worker to quiesce, tar.gz-split <= 1 GiB
# parts, upload to S3, and prune excess backups beyond BACKUP_MAX_COUNT).
#
# Usage:
#   ./scripts/backup-ext4-img-to-s3.sh              # backup ALL provisioned servers (running + stopped)
#   ./scripts/backup-ext4-img-to-s3.sh --all        # backup ALL provisioned servers (running + stopped)
#   ./scripts/backup-ext4-img-to-s3.sh <server_id>  # backup a single server by ID
#
# Environment variables:
#   APP_BASE_URL   API server URL (default: http://localhost:8080)
#   ADMIN_TOKEN    Optional pre-fetched admin Bearer JWT
#   ADMIN_EMAIL    Admin email when ADMIN_TOKEN is unset (default: admin@example.com)
#   ADMIN_PASSWORD Admin password when ADMIN_TOKEN is unset (default: password)
#   POLL_INTERVAL  Seconds between job status polls (default: 2)
#   POLL_TIMEOUT   Max seconds to wait per backup job (default: 1800)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_admin-api-common.sh
source "${SCRIPT_DIR}/_admin-api-common.sh"

if [ "$#" -gt 1 ]; then
	echo "Usage: $0 [--all | <server_id>]" >&2
	exit 1
fi

TARGET="${1:---all}"
APP_BASE_URL="${APP_BASE_URL:-http://localhost:8080}"
APP_BASE_URL="${APP_BASE_URL%/}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-password}"
POLL_INTERVAL="${POLL_INTERVAL:-2}"
POLL_TIMEOUT="${POLL_TIMEOUT:-1800}"

require_commands curl jq
admin_login_if_needed

if [ "$TARGET" = "--all" ]; then
	echo "Triggering immediate ext4 image backups for all provisioned servers..."
	resp="$(curl -fsS -X POST "${APP_BASE_URL}/api/v1/admin/image-backups" \
		-H "Authorization: Bearer ${ADMIN_TOKEN}")"

	mapfile -t job_ids < <(echo "$resp" | jq -r '.jobs[]?.id // empty')
	if [ "${#job_ids[@]}" -eq 0 ]; then
		enqueued="$(echo "$resp" | jq -r '.enqueued // .count // 0')"
		echo "No new backup_image jobs queued (enqueued=${enqueued}; servers may already have a backup running or none are provisioned)."
		exit 0
	fi

	echo "Queued ${#job_ids[@]} backup_image job(s): ${job_ids[*]}"
	failed=0
	for job_id in "${job_ids[@]}"; do
		if ! poll_job "$job_id" "backup_image"; then
			failed=$((failed + 1))
		fi
	done

	if [ "$failed" -gt 0 ]; then
		echo "Error: ${failed} of ${#job_ids[@]} backup_image job(s) failed." >&2
		exit 1
	fi
	echo "All ${#job_ids[@]} ext4 image backup job(s) completed successfully."
	exit 0
fi

echo "Triggering immediate ext4 image backup for server #${TARGET}..."
resp="$(curl -fsS -X POST "${APP_BASE_URL}/api/v1/admin/servers/${TARGET}/image-backups" \
	-H "Authorization: Bearer ${ADMIN_TOKEN}")"

job_id="$(echo "$resp" | jq -r '.job.id // empty')"
if [ -z "$job_id" ]; then
	echo "Backup response: ${resp}"
	exit 0
fi

echo "Queued backup_image job #${job_id}; polling status..."
poll_job "$job_id" "backup_image"
