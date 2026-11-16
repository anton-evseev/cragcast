#!/bin/sh
set -eu

: "${IMAGE:?set IMAGE to the Docker image name and tag}"
: "${WORK_DIR:?set WORK_DIR to a writable directory}"
: "${CLOUDFLARE_API_TOKEN:?set CLOUDFLARE_API_TOKEN to a Cloudflare API token}"
: "${CLOUDFLARE_ACCOUNT_ID:?set CLOUDFLARE_ACCOUNT_ID to a Cloudflare account ID}"

REGION=${REGION:-seattle}
SCRAPE_RETRIES=${SCRAPE_RETRIES:-2}
SCRAPE_RETRY_DELAY=${SCRAPE_RETRY_DELAY:-600}
docker pull "$IMAGE"

mkdir -p "$WORK_DIR"
run_dir=$(mktemp -d "$WORK_DIR/run.XXXXXX")
printf 'Run directory: %s\n' "$run_dir"

# Scrape with retries: the scraper already retries transient HTTP errors, this
# covers longer Open-Meteo outages.
scrape() {
    attempt=0
    until docker run --rm -v "$run_dir:/work" \
        "$IMAGE" scrape --region "$REGION" --out /work/forecast; do
        rc=$?
        attempt=$((attempt + 1))
        if [ "$attempt" -gt "$SCRAPE_RETRIES" ]; then
            return "$rc"
        fi
        printf 'scrape failed (attempt %d/%d); retrying in %ss\n' \
            "$attempt" "$((SCRAPE_RETRIES + 1))" "$SCRAPE_RETRY_DELAY" >&2
        # Drop partial output (created as root by the container).
        docker run --rm --entrypoint rm -v "$run_dir:/work" \
            "$IMAGE" -rf /work/forecast || true
        sleep "$SCRAPE_RETRY_DELAY"
    done
}

status=0
{
    scrape &&
    docker run --rm -v "$run_dir:/work" \
        "$IMAGE" bundle --region "$REGION" --from /work/forecast --out /work/dist &&
    docker run --rm \
        -e CLOUDFLARE_API_TOKEN -e CLOUDFLARE_ACCOUNT_ID \
        -v "$run_dir:/work:ro" \
        "$IMAGE" deploy-cloudflare --from /work/dist
} || status=$?

# Use Docker to remove files created by the containers as root.
docker run --rm --entrypoint rm -v "$WORK_DIR:/work" \
    "$IMAGE" -rf "/work/${run_dir##*/}" || {
    cleanup_status=$?
    if [ "$status" -eq 0 ]; then
        status=$cleanup_status
    fi
}

exit "$status"
