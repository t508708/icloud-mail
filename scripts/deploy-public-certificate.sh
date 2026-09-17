#!/bin/sh
set -eu
umask 077

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lineage=/etc/letsencrypt/live/icloud.us.gooelv.com
if [ "${RENEWED_LINEAGE:-$lineage}" != "$lineage" ]; then
    exit 0
fi

openssl x509 -in "$lineage/fullchain.pem" -noout -checkend 0 \
    -checkhost icloud.us.gooelv.com
openssl x509 -in "$lineage/fullchain.pem" -noout \
    -checkhost imap-icloud.us.gooelv.com
openssl x509 -in "$lineage/fullchain.pem" -noout \
    -checkhost icloud-us.gooelv.com
/www/server/nginx/sbin/nginx -t

# Stage both files before replacing the pair visible through the directory mount.
install -d -m 0750 -o root -g 10001 "$project_dir/.local/tls"
install -m 0640 -o root -g 10001 "$lineage/fullchain.pem" "$project_dir/.local/tls/fullchain.pem.next"
install -m 0640 -o root -g 10001 "$lineage/privkey.pem" "$project_dir/.local/tls/privkey.pem.next"
mv -f "$project_dir/.local/tls/fullchain.pem.next" "$project_dir/.local/tls/fullchain.pem"
mv -f "$project_dir/.local/tls/privkey.pem.next" "$project_dir/.local/tls/privkey.pem"

# The IMAPS process loads its certificate at startup.
cd "$project_dir"
docker compose up -d --no-deps --force-recreate --wait --wait-timeout 90 icloud-api
/www/server/nginx/sbin/nginx -s reload
