#!/usr/bin/env bash
set -euo pipefail

data_dir="${FORGE_DATA_DIR:-/var/lib/exe-image-forge}"
password_file="$data_dir/initial-password"
host="$(hostname)"

cat <<EOF
Exe Image Forge is available at:
  https://${host}.exe.xyz/

Initial admin password:
EOF
if [[ -r "$password_file" ]]; then
  sed 's/^/  /' "$password_file"
else
  echo "  already replaced (use 'exe-image-forge password' to set a new one)"
fi

cat <<'EOF'

The web starts immediately. Base image variants build in the background.
Use Admin > CLI Logins to authenticate GitHub, Codex, Claude, Gemini, and Cloudflare.

Useful status commands:
  systemctl status exe-image-forge-vend.service --no-pager
  systemctl status exe-image-forge-bootstrap.service --no-pager
  journalctl -u exe-image-forge-bootstrap.service -f

Change the generated password:
  exe-image-forge password
EOF
