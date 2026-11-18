#!/usr/bin/env bash
set -euo pipefail

# debug-mount-status.sh
# Lists the mount status, backing loop/image info, propagation, and directory
# health for each /var/lib/mc-platform/mounts/<server-id> directory.

PLATFORM_ROOT="/var/lib/mc-platform"
MOUNTS_DIR="${PLATFORM_ROOT}/mounts"
IMAGES_DIR="${PLATFORM_ROOT}/images"

# print_usage prints CLI usage instructions.
#
# Arguments:
#   None.
#
# Example usage:
#   print_usage
print_usage() {
	cat <<'EOF'
Usage:
  ./debug-mount-status.sh [server-id]

Options:
  server-id   Optional server ID to inspect only /var/lib/mc-platform/mounts/<server-id>
  -h, --help  Show this help message
EOF
}

# inspect_mount_dir prints a formatted status row for a single mount directory.
#
# Arguments:
#   $1: Path to the server mount directory.
#
# Example usage:
#   inspect_mount_dir "/var/lib/mc-platform/mounts/1"
inspect_mount_dir() {
	local dir="$1"
	local server_id
	server_id="$(basename "$dir")"

	local mount_status="UNMOUNTED"
	local health="OK"
	local source="-"
	local fstype="-"
	local propagation="-"
	local backing_file="-"
	local img_status="missing"
	local img_path="${IMAGES_DIR}/${server_id}.img"

	if [ -f "$img_path" ]; then
		local img_size
		img_size="$(du -h "$img_path" 2>/dev/null | awk '{print $1}' || echo "?")"
		img_status="yes(${img_size})"
	fi

	if [ ! -d "$dir" ]; then
		mount_status="NOT_A_DIR"
		health="N/A"
	else
		if ! ls -A "$dir" >/dev/null 2>&1; then
			health="IO_ERROR"
		fi

		if mountpoint -q "$dir" 2>/dev/null; then
			mount_status="MOUNTED"
			source="$(findmnt -n -o SOURCE "$dir" 2>/dev/null || echo "-")"
			fstype="$(findmnt -n -o FSTYPE "$dir" 2>/dev/null || echo "-")"
			propagation="$(findmnt -n -o PROPAGATION "$dir" 2>/dev/null || echo "-")"

			if [[ "$source" == /dev/loop* ]]; then
				backing_file="$(losetup -O BACK-FILE -n "$source" 2>/dev/null || echo "-")"
				[ -n "$backing_file" ] || backing_file="-"
			fi
		fi
	fi

	printf "%-10s %-10s %-9s %-14s %-8s %-10s %-14s %s\n" \
		"$server_id" "$mount_status" "$health" "$source" "$fstype" "$propagation" "$img_status" "$backing_file"
}

main() {
	local filter_id=""

	while [ $# -gt 0 ]; do
		case "$1" in
			-h|--help)
				print_usage
				exit 0
				;;
			-*)
				echo "Error: unknown option: $1" >&2
				print_usage >&2
				exit 1
				;;
			*)
				if [ -z "$filter_id" ]; then
					filter_id="$1"
					shift
				else
					echo "Error: unexpected argument: $1" >&2
					print_usage >&2
					exit 1
				fi
				;;
		esac
	done

	local root_prop="not-mounted"
	if [ -d "$PLATFORM_ROOT" ]; then
		root_prop="$(findmnt -n -o PROPAGATION "$PLATFORM_ROOT" 2>/dev/null || echo "unmounted")"
	fi
	echo "Platform root: $PLATFORM_ROOT (propagation: $root_prop)"
	echo ""

	if [ ! -d "$MOUNTS_DIR" ]; then
		echo "Mounts directory does not exist: $MOUNTS_DIR"
		exit 0
	fi

	local targets=()
	if [ -n "$filter_id" ]; then
		if [ ! -e "${MOUNTS_DIR}/${filter_id}" ]; then
			echo "Mount entry not found: ${MOUNTS_DIR}/${filter_id}" >&2
			exit 1
		fi
		targets+=("${MOUNTS_DIR}/${filter_id}")
	else
		local entry
		for entry in "$MOUNTS_DIR"/*; do
			[ -e "$entry" ] || continue
			targets+=("$entry")
		done
	fi

	if [ ${#targets[@]} -eq 0 ]; then
		echo "No mount directories found in $MOUNTS_DIR."
		exit 0
	fi

	printf "%-10s %-10s %-9s %-14s %-8s %-10s %-14s %s\n" \
		"SERVER_ID" "STATUS" "HEALTH" "SOURCE" "FSTYPE" "PROP" "IMG" "BACKING_FILE"
	printf "%-10s %-10s %-9s %-14s %-8s %-10s %-14s %s\n" \
		"---------" "------" "------" "------" "------" "----" "---" "------------"

	local target
	for target in "${targets[@]}"; do
		inspect_mount_dir "$target"
	done
}

main "$@"
