#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./install.sh [options]

  --pull-host HOST       Registry host shown to clients (default: <hostname>.exe.xyz)
  --image-prefix PREFIX  Local image namespace (default: exe-image-forge)
  --vm-name NAME         exe.dev VM name (default: hostname)
  --help                 Show this help

The installer prompts for a web password and does not build the large images.
Run `exe-image-forge build`, authenticate, and bake after installation.
EOF
}

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
VM_NAME=$(hostname)
PULL_HOST="${VM_NAME}.exe.xyz"
IMAGE_PREFIX="exe-image-forge"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --pull-host)
      [ "$#" -ge 2 ] || { echo "missing value for --pull-host" >&2; exit 2; }
      PULL_HOST=$2
      shift 2
      ;;
    --image-prefix)
      [ "$#" -ge 2 ] || { echo "missing value for --image-prefix" >&2; exit 2; }
      IMAGE_PREFIX=$2
      shift 2
      ;;
    --vm-name)
      [ "$#" -ge 2 ] || { echo "missing value for --vm-name" >&2; exit 2; }
      VM_NAME=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

[[ "$VM_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9-]*$ ]] ||
  { echo "invalid VM name: $VM_NAME" >&2; exit 2; }
[[ "$PULL_HOST" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]+)?$ ]] ||
  { echo "invalid pull host: $PULL_HOST" >&2; exit 2; }
[[ "$IMAGE_PREFIX" =~ ^[a-z0-9][a-z0-9._/-]*$ ]] ||
  { echo "invalid image prefix: $IMAGE_PREFIX" >&2; exit 2; }

for command in curl docker go git python3 sudo; do
  command -v "$command" >/dev/null ||
    { echo "required command not found: $command" >&2; exit 1; }
done
docker buildx version >/dev/null 2>&1 ||
  { echo "Docker Buildx is required" >&2; exit 1; }
id exedev >/dev/null 2>&1 ||
  { echo "the exe.dev 'exedev' user is required" >&2; exit 1; }

EXISTING_INSTALL=0
if sudo test -f /etc/exe-image-forge/config.json; then
  EXISTING_INSTALL=1
  echo "==> existing configuration found; preserving password and host settings"
else
  read -rsp "Web password (minimum 8 characters): " PASSWORD
  echo
  read -rsp "Confirm web password: " PASSWORD_CONFIRM
  echo
  [ "$PASSWORD" = "$PASSWORD_CONFIRM" ] ||
    { echo "passwords do not match" >&2; exit 1; }
  [ "${#PASSWORD}" -ge 8 ] ||
    { echo "password must be at least 8 characters" >&2; exit 1; }
fi

BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"; unset PASSWORD PASSWORD_CONFIRM' EXIT

echo "==> building vending service"
(cd "$ROOT/vend" && go build -trimpath -o "$BUILD_DIR/vend" .)

echo "==> installing files"
sudo install -d -m0755 /opt/exe-image-forge /etc/exe-image-forge
sudo install -d -o exedev -g exedev -m0700 \
  /var/lib/exe-image-forge /var/lib/exe-image-forge/authhome
sudo install -d -m0700 /var/lib/exe-image-forge/registry
sudo install -m0755 "$ROOT/exe-image-forge" /usr/local/bin/exe-image-forge
sudo install -m0755 "$BUILD_DIR/vend" /opt/exe-image-forge/vend

if ! sudo test -f /etc/exe-image-forge/forge.env; then
  ENV_TMP="$BUILD_DIR/forge.env"
  {
    printf 'FORGE_ROOT="%s"\n' "$ROOT"
    printf 'FORGE_IMAGE_PREFIX="%s"\n' "$IMAGE_PREFIX"
    printf 'FORGE_VM_NAME="%s"\n' "$VM_NAME"
    printf 'FORGE_CONFIG="/etc/exe-image-forge/config.json"\n'
    printf 'FORGE_STATE="/var/lib/exe-image-forge/grants.json"\n'
    printf 'FORGE_AUTH_HOME="/var/lib/exe-image-forge/authhome"\n'
    printf 'FORGE_INSTALL_DIR="/opt/exe-image-forge"\n'
    printf 'FORGE_REGISTRY_DATA="/var/lib/exe-image-forge/registry"\n'
    printf 'FORGE_REGISTRY_LOCK="/var/lib/exe-image-forge/registry.lock"\n'
    printf 'FORGE_ADDR="127.0.0.1:8000"\n'
    printf 'FORGE_BASE_IMAGE="%s/base:latest"\n' "$IMAGE_PREFIX"
    printf 'FORGE_COMMAND_PATH="/usr/local/bin/exe-image-forge"\n'
    printf 'FORGE_SERVICE_NAME="exe-image-forge-vend.service"\n'
    printf 'FORGE_REGISTRY_NAME="exe-image-forge-registry"\n'
    printf 'FORGE_MIN_FREE_BYTES="2147483648"\n'
    printf 'FORGE_MAX_DISK_PERCENT="90"\n'
    printf 'FORGE_BUILD_CACHE_MIN_FREE="5gb"\n'
    printf 'FORGE_BUILD_CACHE_RESERVED="2gb"\n'
    printf 'FORGE_ORPHAN_GRACE="2h"\n'
    printf 'COMPRESSION="zstd"\n'
    printf 'COMPRESSION_LEVEL="9"\n'
  } > "$ENV_TMP"
  sudo install -o root -g exedev -m0640 "$ENV_TMP" /etc/exe-image-forge/forge.env
fi

if [ "$EXISTING_INSTALL" = 0 ]; then
  CONFIG_TMP="$BUILD_DIR/config.json"
  python3 - "$CONFIG_TMP" "$PULL_HOST" "$IMAGE_PREFIX" <<'PY'
import json
import sys

path, host, prefix = sys.argv[1:]
base = f"{prefix}/base"
dev = f"{prefix}/dev"
config = {
    "salt": "",
    "hash": "",
    "pull_host": host,
    "repos": [dev, base],
    "source_image": {dev: f"{dev}:latest", base: f"{base}:latest"},
    "ttl_minutes": 30,
    "vm_token": "",
    "auth_home": "/var/lib/exe-image-forge/authhome",
    "dev_image": f"{dev}:latest",
    "passkey_file": "/var/lib/exe-image-forge/passkeys.json",
}
with open(path, "w") as f:
    json.dump(config, f, indent=2)
PY
  sudo install -o exedev -g exedev -m0600 "$CONFIG_TMP" /etc/exe-image-forge/config.json
  printf '%s\n' "$PASSWORD" |
    sudo -u exedev /opt/exe-image-forge/vend \
      -set-password -config /etc/exe-image-forge/config.json >/dev/null
  unset PASSWORD PASSWORD_CONFIRM
fi

for unit in \
  exe-image-forge-vend.service \
  exe-image-forge-gc.service exe-image-forge-gc.timer \
  exe-image-forge-refresh.service exe-image-forge-refresh.timer; do
  sudo install -m0644 "$ROOT/deploy/$unit" "/etc/systemd/system/$unit"
done

if ! docker inspect exe-image-forge-registry >/dev/null 2>&1; then
  echo "==> starting local registry"
  docker run -d --name exe-image-forge-registry --restart always \
    -p 127.0.0.1:5000:5000 \
    -e REGISTRY_STORAGE_DELETE_ENABLED=true \
    -v /var/lib/exe-image-forge/registry:/var/lib/registry \
    registry:2 >/dev/null
fi

sudo systemctl daemon-reload
sudo systemctl enable --now \
  exe-image-forge-vend.service \
  exe-image-forge-gc.timer \
  exe-image-forge-refresh.timer >/dev/null

curl -fsS http://127.0.0.1:8000/healthz >/dev/null

cat <<EOF

Installation complete.

Next:
  exe-image-forge build
  exe-image-forge auth all
  exe-image-forge bake
  ssh exe.dev share port ${VM_NAME} 8000   # run from your laptop

Open: https://${PULL_HOST}/
EOF
