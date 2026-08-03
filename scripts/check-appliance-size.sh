#!/usr/bin/env bash
# Fail a release when either platform exceeds the compressed pull-size budget.
set -euo pipefail

image=${1:?usage: check-appliance-size.sh IMAGE [MAX_BYTES]}
max_bytes=${2:-805306368}
[[ "$max_bytes" =~ ^[0-9]+$ ]] || {
  echo "MAX_BYTES must be an integer" >&2
  exit 2
}

index=$(docker buildx imagetools inspect "$image" --raw)
repo=${image%%@*}
repo=${repo%:*}

failed=0
for platform in linux/amd64 linux/arm64; do
  os=${platform%/*}
  arch=${platform#*/}
  digest=$(jq -r --arg os "$os" --arg arch "$arch" '
    .manifests[]? |
    select(.platform.os == $os and .platform.architecture == $arch) |
    .digest
  ' <<<"$index" | head -1)
  if [[ -z "$digest" ]]; then
    echo "$platform manifest is missing" >&2
    failed=1
    continue
  fi
  manifest=$(docker buildx imagetools inspect "$repo@$digest" --raw)
  bytes=$(jq '[.layers[].size] | add' <<<"$manifest")
  printf '%-12s %12s compressed (%s bytes)\n' \
    "$platform" "$(numfmt --to=iec-i --suffix=B "$bytes")" "$bytes"
  if (( bytes > max_bytes )); then
    echo "$platform exceeds the $(numfmt --to=iec-i --suffix=B "$max_bytes") budget" >&2
    failed=1
  fi
done

exit "$failed"
