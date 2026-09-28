#!/usr/bin/env bash
# Nightly backup of one GitW3 managed hosting host to DigitalOcean Spaces.
#
#   ROLE=forgejo  repositories + registry blobs (/srv/gitw3) and a pg_dump
#   ROLE=manager  Dokploy's own data and the scaler SQLite
#
# Configure an rclone remote named "spaces" (type s3, provider DigitalOcean)
# and set BACKUP_BUCKET. For ROLE=forgejo also set PGURL (postgres://...).
set -euo pipefail

: "${ROLE:?set ROLE to forgejo or manager}" "${BACKUP_BUCKET:?set BACKUP_BUCKET}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
host="$(hostname)"
dest="spaces:$BACKUP_BUCKET/$host"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

case "$ROLE" in
forgejo)
	: "${PGURL:?set PGURL for the Forgejo database dump}"
	pg_dump --format=custom --no-owner --file "$tmp/gitw3-$stamp.dump" "$PGURL"
	rclone copyto "$tmp/gitw3-$stamp.dump" "$dest/postgres/gitw3-$stamp.dump"
	# Repositories and package blobs change incrementally; sync keeps the
	# bucket's versioning as the history.
	rclone sync /srv/gitw3 "$dest/data" --exclude 'tmp/**' --exclude 'sessions/**' --exclude 'queues/**' --transfers 8
	;;
manager)
	if [ -d /etc/dokploy ]; then
		tar -C /etc -czf "$tmp/dokploy-$stamp.tar.gz" dokploy
		rclone copyto "$tmp/dokploy-$stamp.tar.gz" "$dest/dokploy/dokploy-$stamp.tar.gz"
	fi
	# Dokploy keeps its database in the dokploy-postgres service.
	container="$(docker ps -q -f name=dokploy-postgres | head -n1)"
	if [ -n "$container" ]; then
		docker exec "$container" pg_dumpall -U dokploy >"$tmp/dokploy-db-$stamp.sql"
		gzip "$tmp/dokploy-db-$stamp.sql"
		rclone copyto "$tmp/dokploy-db-$stamp.sql.gz" "$dest/dokploy/dokploy-db-$stamp.sql.gz"
	fi
	scaler_db="$(docker volume inspect -f '{{ .Mountpoint }}' gitw3-scaler_state 2>/dev/null || true)"
	if [ -n "$scaler_db" ] && [ -f "$scaler_db/scaler.db" ]; then
		sqlite3 "$scaler_db/scaler.db" ".backup '$tmp/scaler-$stamp.db'"
		rclone copyto "$tmp/scaler-$stamp.db" "$dest/scaler/scaler-$stamp.db"
	fi
	;;
*)
	echo "unknown ROLE $ROLE" >&2
	exit 2
	;;
esac
echo "backup $ROLE $stamp complete"
