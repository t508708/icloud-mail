#!/bin/sh
set -eu
umask 077

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
backup_dir="$project_dir/.local/backups/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$backup_dir"

# Pause writers for a consistent database, key and MIME recovery point.
docker compose stop icloud-api
trap 'docker compose start icloud-api' EXIT
docker compose exec -T postgres /usr/local/bin/icloud-api-postgres-entrypoint backup > "$backup_dir/postgres.dump"
docker compose run --rm --no-deps -T --entrypoint /usr/local/bin/icloud-api-keys-maintenance icloud-api backup > "$backup_dir/keys.tar"
docker compose run --rm --no-deps -T --entrypoint tar icloud-api -C /app/mail-archive -cf - . > "$backup_dir/mail-archive.tar"
if [ -f .local/access.json ]; then
  cp .local/access.json "$backup_dir/access.json"
fi
if [ -f .env ]; then
  cp .env "$backup_dir/deployment.env"
fi
if [ -d .local/tls ]; then
  tar -C .local/tls -cf "$backup_dir/public-tls.tar" .
fi
set -- postgres.dump keys.tar mail-archive.tar
for artifact in access.json deployment.env public-tls.tar; do
  if [ -f "$backup_dir/$artifact" ]; then
    set -- "$@" "$artifact"
  fi
done
(cd "$backup_dir" && sha256sum "$@" > SHA256SUMS)
docker compose start icloud-api
trap - EXIT
printf 'Backup saved: %s\n' "$backup_dir"
