#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${FORGE_ENV_FILE:-/etc/exe-image-forge/forge.env}"
if [[ -r "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

name="${FORGE_REGISTRY_NAME:-exe-image-forge-registry}"
data="${FORGE_REGISTRY_DATA:-/var/lib/exe-image-forge/registry}"

if docker container inspect "$name" >/dev/null 2>&1; then
  running=$(docker container inspect -f '{{.State.Running}}' "$name")
  [[ "$running" = true ]] || docker start "$name" >/dev/null
  exit 0
fi

docker run -d \
  --name "$name" \
  --restart always \
  -p 127.0.0.1:5000:5000 \
  -e REGISTRY_STORAGE_DELETE_ENABLED=true \
  -v "$data:/var/lib/registry" \
  registry:2 >/dev/null
