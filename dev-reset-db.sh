#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Reset all tables and data in the dev PostgreSQL database, then re-run AutoMigrate and user seeding.
echo "Resetting database 'minecraft_hosting' in container mc_postgres..."
docker exec mc_postgres psql -U postgres -d minecraft_hosting -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

# Reset fileserver SQLite database if mc_fileserver is running so user IDs stay in sync with Postgres.
if docker ps -q --filter "name=^/mc_fileserver$" | grep -q .; then
	echo "Resetting fileserver SQLite database in container mc_fileserver..."
	docker exec mc_fileserver sh -c "rm -f /data/fileserver.db*"
	docker restart mc_fileserver >/dev/null
fi

"$SCRIPT_DIR/dev-db-seed.sh"
echo "Database reset complete."
