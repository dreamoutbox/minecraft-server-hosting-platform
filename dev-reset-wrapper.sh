#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"$SCRIPT_DIR/dev-db-reset.sh"
"$SCRIPT_DIR/dev-mounts-reset.sh"

echo "Dev environment reset complete."
