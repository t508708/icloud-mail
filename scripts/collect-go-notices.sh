#!/bin/sh
set -eu

output_dir=${1:?Usage: collect-go-notices.sh OUTPUT_DIR}
mkdir -p "$output_dir/modules"
: > "$output_dir/MISSING.txt"

goroot=$(go env GOROOT)
if [ -f "$goroot/LICENSE" ]; then
    mkdir -p "$output_dir/stdlib"
    cp -p "$goroot/LICENSE" "$output_dir/stdlib/LICENSE"
else
    printf '%s\n' 'standard library LICENSE not found' >"$output_dir/MISSING.txt"
fi

index="$output_dir/modules.tsv"
printf 'module\tversion\tlicense_files\n' >"$index"
module_list=$(mktemp)
trap 'rm -f "$module_list"' EXIT HUP INT TERM
go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}{{end}}' ./cmd/icloud-api > "$module_list"
LC_ALL=C sort -u "$module_list" \
    | while IFS='|' read -r module version dir; do
        [ -n "$module" ] && [ -d "$dir" ] || continue
        destination="$output_dir/modules/$module"
        mkdir -p "$destination"
        find "$dir" -type f \( \
            -iname 'license*' -o -iname 'licence*' -o -iname 'copying*' \
            -o -iname 'notice*' -o -iname 'copyright*' \) \
            ! -name '*.go' ! -name '*.js' ! -name '*.c' ! -name '*.h' \
            -exec sh -c '
                destination=$1
                module_dir=$2
                shift 2
                for file do
                    relative=${file#"$module_dir"/}
                    target="$destination/$relative"
                    mkdir -p "$(dirname "$target")"
                    cp -p "$file" "$target"
                done
            ' sh "$destination" "$dir" {} +
        count=$(find "$destination" -type f | wc -l | tr -d ' ')
        printf '%s\t%s\t%s\n' "$module" "$version" "$count" >>"$index"
        if [ "$count" -eq 0 ]; then
            printf '%s\t%s\n' "$module" "$version" >>"$output_dir/MISSING.txt"
        fi
    done
