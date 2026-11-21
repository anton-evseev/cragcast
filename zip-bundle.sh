#!/bin/sh
set -eu

# Usage: ./zip-bundle.sh [region]
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
region=${1:-seattle}
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '\n==> Scraping %s\n' "$region"
go run . scrape --region "$region" --out "$work_dir/forecast"
printf '\n==> Bundling and checking page sizes (gzip limit 14 KiB)\n'
go run . bundle --region "$region" --from "$work_dir/forecast" --out "$work_dir/bundle" --check-size

printf '\n==> Zipping\n'
archive="$HOME/Downloads/bundle-$region-$(date +%Y%m%d-%H%M%S).zip"
(
    cd "$work_dir/bundle"
    zip -qr "$work_dir/bundle.zip" .
)
mkdir -p "$HOME/Downloads"
mv "$work_dir/bundle.zip" "$archive"
printf 'ZIP saved to %s\n' "$archive"
