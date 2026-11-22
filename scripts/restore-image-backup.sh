#!/usr/bin/env bash
#
# restore-image-backup.sh
# Triggers an admin ext4 .img restore via apiserver (which enqueues a restore_image job;
# jobworker generates presigned S3 GET URLs and invokes cmd/worker to download, merge,
# uncompress, and atomically replace /var/lib/mc-platform/images/<server-id>.img).
#
# Usage:
#   ./scripts/restore-image-backup.sh <server_id> [image_backup_id]
#
# Environment variables:
#   APP_BASE_URL   API server URL (default: http://localhost:8080)
#   ADMIN_TOKEN    Optional pre-fetched admin Bearer JWT
#   ADMIN_EMAIL    Admin email when ADMIN_TOKEN is unset (default: admin@example.com)
#   ADMIN_PASSWORD Admin password when ADMIN_TOKEN is unset (default: password)
#   POLL_INTERVAL  Seconds between job status polls (default: 2)
#   POLL_TIMEOUT   Max seconds to wait for restore completion (default: 1800)

set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "Usage: $0 <server_id> [image_backup_id]" >&2
	exit 1
fi

SERVER_ID="$1"
IMAGE_BACKUP_ID="${2:-}"
APP_BASE_URL="${APP_BASE_URL:-http://localhost:8080}"
APP_BASE_URL="${APP_BASE_URL%/}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-password}"
POLL_INTERVAL="${POLL_INTERVAL:-2}"
POLL_TIMEOUT="${POLL_TIMEOUT:-1800}"

for cmd in curl jq; do
	if ! command -v "$cmd" >/dev/null 2>&1; then
		echo "Error: required command '$cmd' is not installed." >&2
		exit 1
	fi
done

if [ -z "$ADMIN_TOKEN" ]; then
	login_payload="$(jq -n --arg email "$ADMIN_EMAIL" --arg password "$ADMIN_PASSWORD" '{email: $email, password: $password}')"
	login_resp="$(curl -fsS -X POST "${APP_BASE_URL}/api/v1/auth/login" \
		-H "Content-Type: application/json" \
		-d "$login_payload")"
	ADMIN_TOKEN="$(echo "$login_resp" | jq -r '.tokens.access_token // empty')"
	if [ -z "$ADMIN_TOKEN" ]; then
		echo "Error: admin login succeeded but no access_token returned." >&2
		exit 1
	fi
fi

if [ -z "$IMAGE_BACKUP_ID" ]; then
	list_resp="$(curl -fsS "${APP_BASE_URL}/api/v1/admin/servers/${SERVER_ID}/image-backups" \
		-H "Authorization: Bearer ${ADMIN_TOKEN}")"
	IMAGE_BACKUP_ID="$(echo "$list_resp" | jq -r '[.image_backups[] | select(.status == "completed")][0].id // empty')"
	if [ -z "$IMAGE_BACKUP_ID" ]; then
		echo "Error: no completed ext4 image backup found for server ${SERVER_ID}." >&2
		exit 1
	fi
	echo "Selected latest completed image backup #${IMAGE_BACKUP_ID} for server ${SERVER_ID}."
fi

echo "Requesting ext4 image restore: server=${SERVER_ID} image_backup=${IMAGE_BACKUP_ID}..."
restore_resp="$(curl -fsS -X POST \
	"${APP_BASE_URL}/api/v1/admin/servers/${SERVER_ID}/image-backups/${IMAGE_BACKUP_ID}/restore" \
	-H "Authorization: Bearer ${ADMIN_TOKEN}")"

job_id="$(echo "$restore_resp" | jq -r '.job.id // empty')"
if [ -z "$job_id" ]; then
	echo "Restore response: ${restore_resp}"
	exit 0
fi

echo "Queued restore_image job #${job_id}; polling status..."
elapsed=0
while [ "$elapsed" -lt "$POLL_TIMEOUT" ]; do
	job_resp="$(curl -fsS "${APP_BASE_URL}/api/v1/jobs/${job_id}" \
		-H "Authorization: Bearer ${ADMIN_TOKEN}")"
	status="$(echo "$job_resp" | jq -r '.status // empty')"
	case "$status" in
	completed)
		echo "Restore job #${job_id} completed successfully."
		exit 0
		;;
	failed)
		last_err="$(echo "$job_resp" | jq -r '.last_error // "unknown error"')"
		echo "Error: restore job #${job_id} failed: ${last_err}" >&2
		exit 1
		;;
	*)
		echo "  job #${job_id}: status=${status} (${elapsed}s elapsed)"
		sleep "$POLL_INTERVAL"
		elapsed=$((elapsed + POLL_INTERVAL))
		;;
	esac
done

echo "Error: timed out after ${POLL_TIMEOUT}s waiting for restore job #${job_id}." >&2
exit 1
