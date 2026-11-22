#!/usr/bin/env bash
#
# _admin-api-common.sh
# Shared helpers for admin API scripts (dependency checks, admin JWT login, job polling).

require_commands() {
	local cmd
	for cmd in "$@"; do
		if ! command -v "$cmd" >/dev/null 2>&1; then
			echo "Error: required command '$cmd' is not installed." >&2
			exit 1
		fi
	done
}

# admin_login_if_needed populates ADMIN_TOKEN via POST /api/v1/auth/login when unset.
admin_login_if_needed() {
	if [ -n "${ADMIN_TOKEN:-}" ]; then
		return 0
	fi
	local login_payload login_resp
	login_payload="$(jq -n --arg email "$ADMIN_EMAIL" --arg password "$ADMIN_PASSWORD" '{email: $email, password: $password}')"
	login_resp="$(curl -fsS -X POST "${APP_BASE_URL}/api/v1/auth/login" \
		-H "Content-Type: application/json" \
		-d "$login_payload")"
	ADMIN_TOKEN="$(echo "$login_resp" | jq -r '.tokens.access_token // empty')"
	if [ -z "$ADMIN_TOKEN" ]; then
		echo "Error: admin login succeeded but no access_token returned." >&2
		exit 1
	fi
}

# poll_job waits for a queued ServerJob to reach completed or failed status.
#
# Arguments:
#   $1 - job_id
#   $2 - human-readable label (e.g. "backup_image" or "restore_image")
poll_job() {
	local job_id="$1"
	local label="${2:-job}"
	local elapsed=0
	local job_resp status last_err

	while [ "$elapsed" -lt "$POLL_TIMEOUT" ]; do
		job_resp="$(curl -fsS "${APP_BASE_URL}/api/v1/jobs/${job_id}" \
			-H "Authorization: Bearer ${ADMIN_TOKEN}")"
		status="$(echo "$job_resp" | jq -r '.status // empty')"
		case "$status" in
		completed)
			echo "${label} job #${job_id} completed successfully."
			return 0
			;;
		failed)
			last_err="$(echo "$job_resp" | jq -r '.last_error // "unknown error"')"
			echo "Error: ${label} job #${job_id} failed: ${last_err}" >&2
			return 1
			;;
		*)
			echo "  ${label} job #${job_id}: status=${status} (${elapsed}s elapsed)"
			sleep "$POLL_INTERVAL"
			elapsed=$((elapsed + POLL_INTERVAL))
			;;
		esac
	done

	echo "Error: timed out after ${POLL_TIMEOUT}s waiting for ${label} job #${job_id}." >&2
	return 1
}
