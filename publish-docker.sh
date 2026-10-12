#!/bin/sh
set -eu

# Requires Docker with Buildx and an authenticated `docker login`.
# IMAGE is the repository name without a tag.
cd "$(dirname "$0")"
IMAGE=${IMAGE:-antonevseev/cragcast}
tag=$(git rev-parse --short HEAD)

docker buildx build --platform linux/amd64,linux/arm64 \
    -t "$IMAGE:latest" -t "$IMAGE:$tag" --push .
