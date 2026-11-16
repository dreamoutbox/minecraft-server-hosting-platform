#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Reset all tables and data in the dev PostgreSQL database, then re-run AutoMigrate and user seeding.
echo "Resetting database 'minecraft_hosting' in container mc_postgres..."
docker exec mc_postgres psql -U postgres -d minecraft_hosting -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

"$SCRIPT_DIR/dev-db-seed.sh"
echo "Database reset complete."
