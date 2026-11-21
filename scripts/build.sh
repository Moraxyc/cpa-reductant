#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "$0")/.." && pwd)
out=${1:-"$root/dist"}
version=$(tr -d '[:space:]' <"$root/VERSION")

"$root/scripts/prepare.sh"

mkdir -p "$out"
out=$(cd "$out" && pwd)

cd "$root"
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$version" \
  -o "$out/cpa-reductant.so" ./cmd/plugin
