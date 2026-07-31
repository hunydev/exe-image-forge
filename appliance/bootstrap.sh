#!/usr/bin/env bash
# Populate the selectable base images in the background. The web service starts
# independently, so login and administration remain available during this job.
set -euo pipefail

ENV_FILE="${FORGE_ENV_FILE:-/etc/exe-image-forge/forge.env}"
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

marker="${FORGE_DATA_DIR:-/var/lib/exe-image-forge}/bootstrap-complete"
if [[ "${FORGE_AUTO_BUILD:-1}" = 0 ]]; then
  printf 'disabled\n' >"$marker"
  exit 0
fi

if docker image inspect "${FORGE_IMAGE_PREFIX:-exe-image-forge}/base:min" >/dev/null 2>&1; then
  printf 'already-present\n' >"$marker"
  exit 0
fi

exe-image-forge build
printf 'completed=%s\n' "$(date -u +%FT%TZ)" >"$marker"
