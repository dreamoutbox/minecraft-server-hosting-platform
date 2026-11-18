#!/usr/bin/env bash
#
# test-disk-limit.sh
# End-to-end verification that uploading a 1.1 GB random file to a 1 GB ext4 server mount fails cleanly
# without exceeding the allocated disk volume or leaving partial files behind.
#
# Environment variables:
#   APP_URL: Dashboard base URL (default: http://localhost:8080)
#   FILESERVER_URL: Fileserver base URL (default: http://localhost:9100)
#   ADMIN_EMAIL: Admin account email (default: admin@example.com)
#   ADMIN_PASSWORD: Admin account password (default: password)
#   PLAN_NAME: Name of the 1 GB test plan (default: DiskTest1GB)
#   KEEP_SERVER: Set to 1 to skip deleting the temporary test server on exit (default: 0)
#
# Example usage:
#   ./test-disk-limit.sh

set -euo pipefail

APP_URL="${APP_URL:-http://localhost:8080}"
FILESERVER_URL="${FILESERVER_URL:-http://localhost:9100}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-password}"
PLAN_NAME="${PLAN_NAME:-DiskTest1GB}"
KEEP_SERVER="${KEEP_SERVER:-0}"

COOKIE_JAR="$(mktemp)"
LARGE_FILE="$(mktemp /tmp/mc-disk-limit-1.1gb-XXXXXX.bin)"
SERVER_ID=""

cleanup() {
	rm -f "$COOKIE_JAR" "$LARGE_FILE"
	if [ -n "$SERVER_ID" ] && [ "$KEEP_SERVER" != "1" ]; then
		echo "Cleaning up temporary test server #${SERVER_ID}..."
		# Re-authenticate in case cookie jar was already removed, or use fresh cookie if needed
		local clean_jar
		clean_jar="$(mktemp)"
		curl -sS -c "$clean_jar" -b "$clean_jar" \
			-d "email=${ADMIN_EMAIL}" \
			-d "password=${ADMIN_PASSWORD}" \
			"${APP_URL}/login" >/dev/null || true
		curl -sS -b "$clean_jar" -X POST "${APP_URL}/dashboard/servers/${SERVER_ID}/delete" >/dev/null || true
		rm -f "$clean_jar"

		for _ in $(seq 1 30); do
			local count
			count="$(docker exec mc_postgres psql -U postgres -d minecraft_hosting -tAc "SELECT COUNT(*) FROM servers WHERE id = ${SERVER_ID};" || echo "0")"
			if [ "$count" = "0" ]; then
				echo "Temporary test server #${SERVER_ID} deleted."
				break
			fi
			sleep 1
		done
	fi
}
trap cleanup EXIT

require_container() {
	local name="$1"
	if ! docker ps -q --filter "name=^/${name}$" | grep -q .; then
		echo "Error: required container '${name}' is not running." >&2
		exit 1
	fi
}

require_container mc_postgres
require_container mc_apiserver
require_container mc_worker
require_container mc_jobworker
require_container mc_fileserver

for _ in $(seq 1 30); do
	if curl -sf -o /dev/null "${APP_URL}/login" && curl -sf -o /dev/null "${FILESERVER_URL}/health"; then
		break
	fi
	sleep 0.5
done

echo "1. Logging in as admin (${ADMIN_EMAIL})..."
login_code="$(curl -sS -o /dev/null -w "%{http_code}" -c "$COOKIE_JAR" -b "$COOKIE_JAR" \
	-d "email=${ADMIN_EMAIL}" \
	-d "password=${ADMIN_PASSWORD}" \
	"${APP_URL}/login")"
if [ "$login_code" != "302" ]; then
	echo "Error: admin login failed with HTTP ${login_code}" >&2
	exit 1
fi

echo "2. Ensuring 1 GB disk plan '${PLAN_NAME}' exists via admin plans endpoint..."
plan_id="$(docker exec mc_postgres psql -U postgres -d minecraft_hosting -tAc "SELECT id FROM plans WHERE name = '${PLAN_NAME}' LIMIT 1;")"
if [ -z "$plan_id" ]; then
	create_plan_code="$(curl -sS -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
		-d "name=${PLAN_NAME}" \
		-d "cpu_limit=1" \
		-d "mem_limit_mb=1024" \
		-d "disk_limit_gb=1" \
		-d "price_cents=100" \
		"${APP_URL}/dashboard/admin/plans")"
	if [ "$create_plan_code" != "302" ]; then
		echo "Error: failed creating 1 GB plan (HTTP ${create_plan_code})" >&2
		exit 1
	fi
	plan_id="$(docker exec mc_postgres psql -U postgres -d minecraft_hosting -tAc "SELECT id FROM plans WHERE name = '${PLAN_NAME}' LIMIT 1;")"
fi
if [ -z "$plan_id" ]; then
	echo "Error: could not resolve ID for plan '${PLAN_NAME}'" >&2
	exit 1
fi
echo "   Using plan #${plan_id} (${PLAN_NAME}, 1 GB disk)."

echo "3. Creating test server with 1 GB plan..."
srv_name="disktest-$(date +%s)"
create_srv_loc="$(curl -sS -o /dev/null -w "%{redirect_url}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
	-d "name=${srv_name}" \
	-d "plan_id=${plan_id}" \
	"${APP_URL}/dashboard/servers/new")"
SERVER_ID="$(printf '%s\n' "$create_srv_loc" | sed -n 's|.*/dashboard/servers/\([0-9][0-9]*\).*|\1|p')"
if [ -z "$SERVER_ID" ]; then
	echo "Error: failed queueing test server creation (redirect=${create_srv_loc})" >&2
	exit 1
fi
echo "   Queued server #${SERVER_ID} (${srv_name}). Waiting for running status..."

srv_status=""
for _ in $(seq 1 90); do
	srv_status="$(docker exec mc_postgres psql -U postgres -d minecraft_hosting -tAc "SELECT status FROM servers WHERE id = ${SERVER_ID};")"
	if [ "$srv_status" = "running" ]; then
		break
	fi
	if [ "$srv_status" = "error" ]; then
		echo "Error: test server #${SERVER_ID} entered error state." >&2
		exit 1
	fi
	sleep 1
done
if [ "$srv_status" != "running" ]; then
	echo "Error: timed out waiting for server #${SERVER_ID} (status=${srv_status})" >&2
	exit 1
fi

MOUNT_PATH="/var/lib/mc-platform/mounts/${SERVER_ID}"
echo "   Server #${SERVER_ID} running. Mount stats:"
df -h "$MOUNT_PATH"

echo "4. Generating 1.1 GB random file at ${LARGE_FILE}..."
# 1.1 GB = 1100 MiB (1153433600 bytes), exceeding a 1 GiB ext4 loop filesystem.
dd if=/dev/urandom of="$LARGE_FILE" bs=1M count=1100 status=none
file_bytes="$(stat -c '%s' "$LARGE_FILE")"
echo "   Created ${LARGE_FILE} (${file_bytes} bytes)."

echo "5. Opening fileserver session for server #${SERVER_ID}..."
fs_open_code="$(curl -sS -L -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
	"${APP_URL}/dashboard/servers/${SERVER_ID}/files/open")"
if [ "$fs_open_code" != "200" ]; then
	echo "Error: failed opening fileserver session (HTTP ${fs_open_code})" >&2
	exit 1
fi

echo "6. Uploading 1.1 GB file to ${FILESERVER_URL}/upload (expected to fail with disk full)..."
upload_redirect="$(curl -sS -o /dev/null -w "%{redirect_url}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
	-F "path=${MOUNT_PATH}" \
	-F "file=@${LARGE_FILE}" \
	"${FILESERVER_URL}/upload")"

echo "   Upload redirect: ${upload_redirect}"
if ! printf '%s\n' "$upload_redirect" | grep -q "error="; then
	echo "FAIL: upload unexpectedly succeeded or returned no error redirect!" >&2
	exit 1
fi
if printf '%s\n' "$upload_redirect" | grep -qi "upload+exceeded+limit"; then
	echo "FAIL: upload stopped at HTTP maxUploadBytes limit instead of hitting ext4 disk limit!" >&2
	exit 1
fi
if ! printf '%s\n' "$upload_redirect" | grep -qiE "no\+space\+left\+on\+device|failed\+writing\+uploaded\+file"; then
	echo "FAIL: unexpected upload error redirect: ${upload_redirect}" >&2
	exit 1
fi

uploaded_name="$(basename "$LARGE_FILE")"
if [ -e "${MOUNT_PATH}/${uploaded_name}" ]; then
	echo "FAIL: partial uploaded file ${MOUNT_PATH}/${uploaded_name} was not cleaned up!" >&2
	exit 1
fi

echo "PASS: 1.1 GB upload failed due to ext4 disk space limit and no partial file remains."
df -h "$MOUNT_PATH"
