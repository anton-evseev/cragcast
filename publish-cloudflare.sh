#!/bin/sh
set -eu

: "${IMAGE:?set IMAGE to the Docker image name and tag}"
: "${WORK_DIR:?set WORK_DIR to a writable directory}"
: "${CLOUDFLARE_API_TOKEN:?set CLOUDFLARE_API_TOKEN to a Cloudflare API token}"
: "${CLOUDFLARE_ACCOUNT_ID:?set CLOUDFLARE_ACCOUNT_ID to a Cloudflare account ID}"

REGION=${REGION:-seattle}
docker pull "$IMAGE"

mkdir -p "$WORK_DIR"
run_dir=$(mktemp -d "$WORK_DIR/run.XXXXXX")
printf 'Run directory: %s\n' "$run_dir"

docker run --rm -v "$run_dir:/work" \
    "$IMAGE" scrape --region "$REGION" --out /work/forecast

docker run --rm -v "$run_dir:/work" \
    "$IMAGE" bundle --region "$REGION" --from /work/forecast --out /work/dist

docker run --rm \
    --entrypoint wrangler --workdir /work \
    -e HOME=/tmp -e CLOUDFLARE_API_TOKEN -e CLOUDFLARE_ACCOUNT_ID \
    -v "$run_dir:/work" -v "$run_dir/dist:/app/dist:ro" \
    "$IMAGE" deploy --config /app/wrangler.jsonc
