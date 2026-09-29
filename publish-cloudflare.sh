#!/bin/sh
set -eu

: "${IMAGE:?set IMAGE to the Docker image name and tag}"
: "${WORK_DIR:?set WORK_DIR to a writable directory}"
: "${USER_AGENT:?set USER_AGENT for weather requests}"

REGION=${REGION:-seattle}
project_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
mkdir -p "$WORK_DIR"
run_dir=$(mktemp -d "$WORK_DIR/run.XXXXXX")
printf 'Run directory: %s\n' "$run_dir"

docker run --rm --user "$(id -u):$(id -g)" -v "$run_dir:/work" \
    "$IMAGE" scrape --region "$REGION" --out /work/forecast \
    --user-agent "$USER_AGENT"

docker run --rm --user "$(id -u):$(id -g)" -v "$run_dir:/work" \
    "$IMAGE" bundle --region "$REGION" --from /work/forecast --out /work/dist

cp "$project_dir/wrangler.jsonc" "$run_dir/wrangler.jsonc"
(
    cd "$run_dir"
    wrangler deploy --config ./wrangler.jsonc
)
