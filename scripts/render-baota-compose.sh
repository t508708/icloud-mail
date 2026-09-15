#!/bin/bash
set -euo pipefail

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"

if [ "$#" -eq 0 ]; then
  set -- compose.yaml compose.public.yaml
fi
compose_args=()
for compose_file in "$@"; do
  compose_args+=(-f "$compose_file")
done

temp_file=$(mktemp "$project_dir/.compose.baota.XXXXXX.yaml")
trap 'rm -f -- "$temp_file"' EXIT

# Keep placeholders and relative paths so no deployment secrets enter the file.
docker compose "${compose_args[@]}" config \
  --no-interpolate --no-env-resolution --no-path-resolution --no-normalize \
  --output "$temp_file"
docker compose -f "$temp_file" config --quiet
chmod 644 "$temp_file"
mv -- "$temp_file" "$project_dir/compose.baota.yaml"
printf 'Generated %s/compose.baota.yaml\n' "$project_dir"
