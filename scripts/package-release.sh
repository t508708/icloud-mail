#!/usr/bin/env bash
set -euo pipefail
umask 077

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
release_tag=${1:?Usage: bash scripts/package-release.sh TAG}
[[ "$release_tag" =~ ^handoff-[0-9]{4}\.[0-9]{2}\.[0-9]{2}([.-][a-zA-Z0-9]+)*$ ]] || {
    printf '%s\n' 'Expected a handoff-YYYY.MM.DD tag.' >&2
    exit 1
}
for tool in git docker tar zip gzip sha256sum flock node; do
    command -v "$tool" >/dev/null
done
commit=$(git rev-parse --verify "refs/tags/$release_tag^{commit}")
[[ -z "$(git status --porcelain)" ]] || {
    printf '%s\n' 'Commit tracked changes before creating a release.' >&2
    exit 1
}
mkdir -p .local/releases
exec 9>.local/project-heavy.lock
flock 9
available_kib=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
load_one=$(awk '{print $1}' /proc/loadavg)
if (( available_kib < 2097152 )) || ! awk -v task_load="$load_one" 'BEGIN {exit !(task_load <= 8)}'; then
    printf '%s\n' 'Resource guard: retry when MemAvailable >= 2 GiB and load1 <= 8.' >&2
    exit 1
fi

app_image="icloud-api:$release_tag"
database_image="icloud-api-postgres:$release_tag"
for image in "$app_image" "$database_image"; do
    [[ "$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')" == linux/amd64 ]]
done
output_dir="$project_dir/.local/releases/$release_tag"
[[ ! -e "$output_dir" ]] || { printf 'Release already exists: %s\n' "$output_dir" >&2; exit 1; }
staging=$(mktemp -d "$project_dir/.local/releases/.package-XXXXXX")
trap 'rm -rf -- "$staging"' EXIT
package_name="icloud-mail-$release_tag"
package_dir="$staging/$package_name"
mkdir -p "$package_dir" "$staging/output"
git archive --format=tar "$release_tag" | tar -xf - -C "$package_dir"

# Only tracked, exportable source enters the archive. No host data or Git history.
node --input-type=module - "$package_dir" <<'JS'
import fs from 'node:fs';
import path from 'node:path';
const root = process.argv[2];
const blockedNames = new Set(['.local', '.git', '.env', 'node_modules', 'access.json']);
const violations = [];
function walk(directory) {
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const full = path.join(directory, entry.name);
    const relative = path.relative(root, full);
    if (blockedNames.has(entry.name) || entry.isSymbolicLink() || /\.(?:db|sqlite|pem|key|p12|pfx)$/i.test(entry.name)) {
      violations.push(relative);
    } else if (entry.isDirectory()) {
      walk(full);
    } else {
      const text = fs.readFileSync(full, 'utf8');
      if (/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----\r?\n[A-Za-z0-9+/=\r\n]{32,}-----END|cfut_[A-Za-z0-9]{20,}/.test(text)) {
        // Do not print matching contents, even if they are test fixtures.
        violations.push(relative);
      }
    }
  }
}
walk(root);
if (violations.length) {
  console.error(JSON.stringify({archive_check: 'failed', paths: violations}));
  process.exit(1);
}
console.log(JSON.stringify({archive_check: 'passed'}));
JS

# The image IDs are recorded separately: packaging commits may only change docs.
app_id=$(docker image inspect "$app_image" --format '{{.Id}}')
database_id=$(docker image inspect "$database_image" --format '{{.Id}}')
printf '{"release":"%s","source_commit":"%s","platform":"linux/amd64","app_image":"%s","app_image_id":"%s","database_image":"%s","database_image_id":"%s","includes_user_data":false}\n' \
    "$release_tag" "$commit" "$app_image" "$app_id" "$database_image" "$database_id" > "$package_dir/RELEASE.json"
(
    cd "$package_dir"
    find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS
)
(
    cd "$staging"
    zip -q -r "output/$package_name-source.zip" "$package_name"
)
mkdir -p "$package_dir/images"
docker image save "$app_image" "$database_image" | gzip -1 > "$package_dir/images/linux-amd64.tar.gz"
(
    cd "$package_dir"
    sha256sum images/linux-amd64.tar.gz >> SHA256SUMS
)
(
    cd "$staging"
    zip -q -0 -r "output/$package_name-linux-amd64.zip" "$package_name"
)
(
    cd "$staging/output"
    sha256sum ./*.zip > SHA256SUMS
)
mv "$staging/output" "$output_dir"
printf 'Release created: %s\nSource commit: %s\n' "$output_dir" "$commit"
