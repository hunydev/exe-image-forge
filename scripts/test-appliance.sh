#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT

etc_dir="$test_dir/etc"
data_dir="$test_dir/data"
vend_binary="$test_dir/vend"

(cd "$repo/vend" && go build -trimpath -o "$vend_binary" .)

FORGE_ETC_DIR="$etc_dir" \
FORGE_DATA_DIR="$data_dir" \
FORGE_SOURCE_ROOT="/opt/test/source" \
FORGE_VEND_BINARY="$vend_binary" \
FORGE_HOSTNAME="forge-test" \
FORGE_IMAGE_PREFIX="test-forge" \
FORGE_AUTO_BUILD=0 \
  "$repo/appliance/initialize.sh"

config="$etc_dir/config.json"
env_file="$etc_dir/forge.env"
password_file="$data_dir/initial-password"

jq -e '
  .pull_host == "forge-test.exe.xyz" and
  .repos == ["test-forge/dev", "test-forge/base"] and
  .auth_home == $auth_home and
  (.salt | length) == 32 and
  (.hash | length) == 64
' --arg auth_home "$data_dir/authhome" "$config" >/dev/null

grep -F 'FORGE_ADDR="127.0.0.1:8000"' "$env_file" >/dev/null
grep -F 'FORGE_TERMINAL_MODE="auth-host"' "$env_file" >/dev/null
grep -F 'FORGE_AUTO_BUILD="0"' "$env_file" >/dev/null
[[ -d "$data_dir/authhome/.config/.wrangler/config" ]]

# The public appliance must stay independent from the general-purpose exeuntu
# workstation; inheriting it was the source of the 1.54 GiB compressed image.
grep -F 'FROM docker.io/library/ubuntu:24.04@sha256:' \
  "$repo/appliance/Dockerfile" >/dev/null
if grep -F 'FROM ghcr.io/boldsoftware/exeuntu' "$repo/appliance/Dockerfile" >/dev/null; then
  echo "appliance unexpectedly inherits the full exeuntu image" >&2
  exit 1
fi
grep -F 'TOOLS="codex claude gemini gh wrangler"' \
  "$repo/appliance/Dockerfile" >/dev/null

password=$(<"$password_file")
[[ "${#password}" -ge 32 ]]
if grep -F "$password" "$config" >/dev/null; then
  echo "plaintext password leaked into config" >&2
  exit 1
fi

before=$(sha256sum "$config" "$env_file" "$password_file")
FORGE_ETC_DIR="$etc_dir" \
FORGE_DATA_DIR="$data_dir" \
FORGE_SOURCE_ROOT="/changed" \
FORGE_VEND_BINARY="$vend_binary" \
FORGE_HOSTNAME="changed-host" \
  "$repo/appliance/initialize.sh"
after=$(sha256sum "$config" "$env_file" "$password_file")
[[ "$before" = "$after" ]]

echo "appliance initialization test passed"
