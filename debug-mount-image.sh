#!/usr/bin/env bash
set -euo pipefail

# debug-mount-image.sh
# Manually mounts or unmounts /var/lib/mc-platform/images/<server-id>.img
# at an isolated inspection directory for debugging.

IMAGES_DIR="/var/lib/mc-platform/images"
DEFAULT_DEBUG_ROOT="/tmp/mc-debug-mounts"

# print_usage prints CLI usage instructions and examples.
#
# Arguments:
#   None.
#
# Example usage:
#   print_usage
print_usage() {
	cat <<'EOF'
Usage:
  ./debug-mount-image.sh <server-id> [mount|umount] [--rw] [--dir <mount-dir>]

Options:
  mount         Mount the server image for inspection (default)
  umount        Unmount the debug mount directory and detach loop device
  --rw          Mount read-write instead of read-only (default: read-only)
  --dir <path>  Override target mount directory (default: /tmp/mc-debug-mounts/<server-id>)
  -h, --help    Show this help message

Examples:
  ./debug-mount-image.sh 1
  ./debug-mount-image.sh 1 mount --rw
  ./debug-mount-image.sh 1 umount
EOF
}

# mount_image validates the server image is not already mounted elsewhere and mounts it.
#
# Arguments:
#   $1: Server ID.
#   $2: Path to the .img file.
#   $3: Target mount directory.
#   $4: Mount mode ("ro" or "rw").
#
# Example usage:
#   mount_image "1" "/var/lib/mc-platform/images/1.img" "/tmp/mc-debug-mounts/1" "ro"
mount_image() {
	local server_id="$1"
	local img_path="$2"
	local mount_dir="$3"
	local mode="$4"

	if [ ! -f "$img_path" ]; then
		echo "Error: image file not found: $img_path" >&2
		exit 1
	fi

	local existing_loops
	existing_loops="$(losetup -j "$img_path" 2>/dev/null || true)"
	if [ -n "$existing_loops" ]; then
		echo "Warning: $img_path is already attached to loop device(s):" >&2
		echo "$existing_loops" >&2
		local loop_dev active_target
		while IFS= read -r line; do
			loop_dev="${line%%:*}"
			[ -n "$loop_dev" ] || continue
			active_target="$(findmnt -n -o TARGET --source "$loop_dev" 2>/dev/null || true)"
			if [ -n "$active_target" ]; then
				echo "Error: image is already mounted at $active_target ($loop_dev). Refusing to double-mount." >&2
				exit 1
			fi
		done <<< "$existing_loops"
	fi

	if mountpoint -q "$mount_dir" 2>/dev/null; then
		echo "Error: $mount_dir is already a mountpoint." >&2
		findmnt "$mount_dir" || true
		exit 1
	fi

	mkdir -p "$mount_dir"
	echo "Mounting $img_path -> $mount_dir ($mode)..."
	sudo mount -o "loop,${mode}" "$img_path" "$mount_dir"

	echo "Mounted successfully:"
	findmnt -o TARGET,SOURCE,FSTYPE,OPTIONS "$mount_dir" || true
	echo ""
	echo "Top-level contents:"
	ls -la "$mount_dir" || true
	echo ""
	echo "When finished inspecting, run:"
	if [ "$mount_dir" = "${DEFAULT_DEBUG_ROOT}/${server_id}" ]; then
		echo "  ./debug-mount-image.sh $server_id umount"
	else
		echo "  ./debug-mount-image.sh $server_id umount --dir $mount_dir"
	fi
}

# umount_image unmounts the debug mount directory and cleans up the empty directory.
#
# Arguments:
#   $1: Target mount directory.
#
# Example usage:
#   umount_image "/tmp/mc-debug-mounts/1"
umount_image() {
	local mount_dir="$1"

	if ! mountpoint -q "$mount_dir" 2>/dev/null; then
		echo "$mount_dir is not currently mounted."
		if [ -d "$mount_dir" ]; then
			rmdir "$mount_dir" 2>/dev/null || true
		fi
		return 0
	fi

	echo "Unmounting $mount_dir..."
	sudo umount -d "$mount_dir"
	rmdir "$mount_dir" 2>/dev/null || true
	echo "Unmounted $mount_dir."
}

main() {
	if [ $# -lt 1 ]; then
		print_usage >&2
		exit 1
	fi

	local server_id=""
	local action="mount"
	local mode="ro"
	local custom_dir=""

	while [ $# -gt 0 ]; do
		case "$1" in
			-h|--help)
				print_usage
				exit 0
				;;
			mount|umount)
				action="$1"
				shift
				;;
			--rw)
				mode="rw"
				shift
				;;
			--dir)
				if [ $# -lt 2 ]; then
					echo "Error: --dir requires a path argument." >&2
					exit 1
				fi
				custom_dir="$2"
				shift 2
				;;
			-*)
				echo "Error: unknown option: $1" >&2
				print_usage >&2
				exit 1
				;;
			*)
				if [ -z "$server_id" ]; then
					server_id="$1"
					shift
				else
					echo "Error: unexpected argument: $1" >&2
					print_usage >&2
					exit 1
				fi
				;;
		esac
	done

	if [ -z "$server_id" ]; then
		echo "Error: <server-id> is required." >&2
		print_usage >&2
		exit 1
	fi

	local img_path="${IMAGES_DIR}/${server_id}.img"
	local mount_dir="${custom_dir:-${DEFAULT_DEBUG_ROOT}/${server_id}}"

	case "$action" in
		mount)
			mount_image "$server_id" "$img_path" "$mount_dir" "$mode"
			;;
		umount)
			umount_image "$mount_dir"
			;;
	esac
}

main "$@"
