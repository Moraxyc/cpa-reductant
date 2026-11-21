#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "$0")/.." && pwd)
target="$root/.cache/magpie"
lockfile="$root/flake.lock"

mkdir -p "$root/.cache"

if [[ -n ${MAGPIE_PATCHED_SRC:-} && -d $MAGPIE_PATCHED_SRC ]]; then
  rm -rf "$target"
  ln -s "$MAGPIE_PATCHED_SRC" "$target"
  exit 0
fi

magpie_rev=$(
  if command -v jq >/dev/null 2>&1; then
    jq -er '.nodes.magpie.locked.rev' "$lockfile"
  else
    sed -n '/"magpie": {/,/"original":/ { /"rev":/ { s/.*"rev": "\([^"]*\)".*/\1/p; q; } }' "$lockfile"
  fi
)

if [[ -z $magpie_rev ]]; then
  echo "failed to read magpie locked rev from $lockfile" >&2
  exit 1
fi

if [[ -d $target && -f "$target/.prepared-$magpie_rev" ]]; then
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [[ -n ${MAGPIE_SOURCE_CACHE:-} && -d $MAGPIE_SOURCE_CACHE ]]; then
  cp -R "$MAGPIE_SOURCE_CACHE" "$work/magpie"
else
  mkdir -p "$work/magpie"
  curl -fsSL --retry 2 "https://codeload.github.com/yetone/magpie/tar.gz/$magpie_rev" |
    tar -xz --strip-components=1 -C "$work/magpie"
fi

for patch in "$root"/patches/magpie/*.patch; do
  [[ -e $patch ]] || continue
  git -C "$work/magpie" apply "$patch"
done

chmod -R u+w "$work/magpie"
touch "$work/magpie/.prepared-$magpie_rev"
rm -rf "$target"
mv "$work/magpie" "$target"
