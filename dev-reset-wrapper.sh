#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"$SCRIPT_DIR/dev-reset-db.sh"
"$SCRIPT_DIR/dev-reset-mounts.sh"
"$SCRIPT_DIR/dev-docker-compose-down.sh"
"$SCRIPT_DIR/dev-docker-compose-up.sh"

echo "Dev environment reset complete."
