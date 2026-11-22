#!/usr/bin/env bash
# Inspect all tables and rows in the fileserver SQLite database.
#
# Usage:
#   ./fileserver/dev-inspect-sqlite.sh [local-db-path]
#   ./fileserver/dev-inspect-sqlite.sh --container mc_fileserver [--db-path /data/fileserver.db]
#
# Examples:
#   ./fileserver/dev-inspect-sqlite.sh
#   ./fileserver/dev-inspect-sqlite.sh ./data/fileserver.db
#   ./fileserver/dev-inspect-sqlite.sh --container my_fileserver

set -euo pipefail

DEFAULT_CONTAINER="mc_fileserver"
DEFAULT_CONTAINER_DB_PATH="/data/fileserver.db"

CONTAINER="${DEFAULT_CONTAINER}"
CONTAINER_DB_PATH="${DEFAULT_CONTAINER_DB_PATH}"
LOCAL_DB_PATH=""
TMP_DIR=""

usage() {
	cat <<'EOF'
Usage:
  dev-inspect-sqlite.sh [local-db-path]
  dev-inspect-sqlite.sh [--container <name>] [--db-path <container-db-path>]

Options:
  -c, --container <name>   Docker container name to copy the SQLite DB from (default: mc_fileserver)
  -d, --db-path <path>     Path to SQLite DB inside the container (default: /data/fileserver.db)
  -h, --help               Show this help message

Examples:
  ./fileserver/dev-inspect-sqlite.sh
  ./fileserver/dev-inspect-sqlite.sh ./fileserver.db
  ./fileserver/dev-inspect-sqlite.sh --container mc_fileserver
EOF
}

cleanup() {
	if [[ -n "${TMP_DIR}" && -d "${TMP_DIR}" ]]; then
		rm -rf "${TMP_DIR}"
	fi
}

trap cleanup EXIT

parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
			-c|--container)
				if [[ $# -lt 2 ]]; then
					echo "Error: $1 requires a container name" >&2
					exit 1
				fi
				CONTAINER="$2"
				shift 2
				;;
			-d|--db-path)
				if [[ $# -lt 2 ]]; then
					echo "Error: $1 requires a path" >&2
					exit 1
				fi
				CONTAINER_DB_PATH="$2"
				shift 2
				;;
			-h|--help)
				usage
				exit 0
				;;
			-*)
				echo "Error: unknown option: $1" >&2
				usage >&2
				exit 1
				;;
			*)
				if [[ -n "${LOCAL_DB_PATH}" ]]; then
					echo "Error: unexpected extra argument: $1" >&2
					usage >&2
					exit 1
				fi
				LOCAL_DB_PATH="$1"
				shift
				;;
		esac
	done
}

require_cmd() {
	local cmd="$1"
	if ! command -v "${cmd}" >/dev/null 2>&1; then
		echo "Error: required command '${cmd}' is not installed." >&2
		exit 1
	fi
}

copy_db_from_container() {
	require_cmd docker
	TMP_DIR="$(mktemp -d -t fileserver-sqlite-XXXXXX)"
	local dest="${TMP_DIR}/fileserver.db"

	echo "Copying ${CONTAINER}:${CONTAINER_DB_PATH} ..."
	docker cp "${CONTAINER}:${CONTAINER_DB_PATH}" "${dest}"
	# Copy WAL/SHM sidecar files if present so recent uncommitted WAL frames are visible.
	docker cp "${CONTAINER}:${CONTAINER_DB_PATH}-wal" "${dest}-wal" 2>/dev/null || true
	docker cp "${CONTAINER}:${CONTAINER_DB_PATH}-shm" "${dest}-shm" 2>/dev/null || true

	LOCAL_DB_PATH="${dest}"
}

inspect_database() {
	local db_file="$1"
	if [[ ! -f "${db_file}" ]]; then
		echo "Error: SQLite database file not found: ${db_file}" >&2
		exit 1
	fi

	local tables
	tables="$(sqlite3 "${db_file}" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;")"

	if [[ -z "${tables}" ]]; then
		echo "No user tables found in ${db_file}."
		return 0
	fi

	echo "Database: ${db_file}"
	echo

	local table row_count
	while IFS= read -r table; do
		[[ -z "${table}" ]] && continue
		row_count="$(sqlite3 "${db_file}" "SELECT COUNT(*) FROM \"${table}\";")"
		echo "=== ${table} (${row_count} rows) ==="
		if [[ "${row_count}" -gt 0 ]]; then
			sqlite3 -header -column "${db_file}" "SELECT * FROM \"${table}\";"
		else
			echo "(empty)"
		fi
		echo
	done <<< "${tables}"
}

main() {
	parse_args "$@"
	require_cmd sqlite3

	if [[ -z "${LOCAL_DB_PATH}" ]]; then
		copy_db_from_container
	fi

	inspect_database "${LOCAL_DB_PATH}"
}

main "$@"
