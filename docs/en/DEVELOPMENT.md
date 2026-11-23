**English** | [简体中文](../zh/DEVELOPMENT.md)

# Developer Guide

## Layout & Architecture

`cpa-reductant` builds as a Go C-ABI shared library (`-buildmode=c-shared`) loaded by CLIProxyAPI:

- `patches/magpie/` — patches (`0001-expose-redact.patch`, `0002-expose-guard.patch`) applied to the `yetone/magpie` commit pinned in `flake.lock` to expose `pkg/redact` and `pkg/guard` under `.cache/magpie/` (wired via `replace` in `go.mod`).
- `cmd/plugin/` — CPA C-ABI bridge (`abi.h`, `abi.c`) and `c-shared` entry point (`main.go`).
- `internal/config/` — YAML parsing, validation, policy merging, and model/protocol routing.
- `internal/engine/` — redaction/restoration orchestrator, local/HTTP PII inference scheduling and caching, `CallerScope` isolation, webhook alerts, Prometheus metrics, weight downloader, and debug capture.
- `internal/adapter/` — CPA lifecycle adapter (`register`, `reconfigure`, `route`, `execute`, `execute_stream`, `management`), SSE stream normalization, and management UI.
- `scripts/` — `prepare.sh` (fetches and patches `.cache/magpie`) and `build.sh` (compiles `dist/cpa-reductant.so`).
- `test/` — end-to-end smoke tests against the real `cliproxyapi` binary and local `httptest` mocks.
- `flake.nix`, `dev/`, `nix/` — Nix builds, `treefmt`, Zig CC cross-compilation, and smoke checks.
- `.github/workflows/` — `ci.yml`, `release.yml`, and `upstream-watch.yml`.

## Building

### With Nix

The dev shell's `shellHook` links the patched Magpie tree to `.cache/magpie`:

```sh
nix develop --accept-flake-config
go test -race ./internal/...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

Common Nix commands:

```sh
nix build --accept-flake-config         # Current platform
nix flake check --print-build-logs      # Unit tests, smoke test, portable builds
nix fmt                                 # gofmt + nixfmt
```

Cross-compile portable binaries from Linux via Zig CC:

```sh
nix build .#portable-linux-amd64
nix build .#portable-linux-arm64
nix build .#portable-windows-amd64
nix build .#portable-windows-arm64
```

Binary cache:

- Substituter: `https://nix-cache.qaq.li/cpa-reductant`
- Trusted Public Key: `cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo=`

### Without Nix

Requires Go `1.26.10+`, a C compiler (`gcc`/`clang`), `curl`, `tar`, and `git` (optional: `jq`).

```sh
bash scripts/build.sh
```

`build.sh` runs `scripts/prepare.sh` (fetching the Magpie commit from `flake.lock` and applying `patches/magpie/*.patch`), injects `VERSION` into `internal/config.Version`, and writes `dist/cpa-reductant.so`.

Manual build:

```sh
bash scripts/prepare.sh
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$(tr -d '[:space:]' < VERSION)" \
  -o dist/cpa-reductant.so ./cmd/plugin
```

`prepare.sh` overrides:

- `MAGPIE_PATCHED_SRC` — symlink to an already-patched Magpie directory
- `MAGPIE_SOURCE_CACHE` — local unpatched Magpie checkout

## Testing & Benchmarks

```sh
go test -race ./internal/...
go test -run='^$' -bench=. -benchmem ./internal/engine ./internal/adapter
CPA_BIN=/path/to/cliproxyapi go test -v ./test/...
```

Set `CPA_PLUGIN_PATH` to reuse an already-built shared library in `test/smoke_test.go`; otherwise the test compiles one on the fly.

## CI & Releases

- `ci.yml` — runs `nix flake check`, Linux/Windows portable builds, non-Nix `scripts/build.sh` + `go vet`, and macOS (`amd64`/`arm64`) unit tests and `.dylib` builds.
- `upstream-watch.yml` — runs Monday and Thursday (or manually) to check `yetone/magpie` and `router-for-me/CLIProxyAPI` releases, dry-run `patches/magpie/*.patch`, and test against latest `nixpkgs` `cliproxyapi`. Opens/updates an `upstream-sync` issue when changes are needed and closes it once aligned.
- `release.yml` — triggered by `v*` tags (must match `VERSION`). Builds all 6 platform archives (`cpa-reductant_<version>_<os>_<arch>.zip`) plus `checksums.txt` and publishes to GitHub Releases.

## Upstream Upgrades

### Upgrading Magpie (`yetone/magpie`)

1. Bump the tag in `inputs.magpie.url` (`flake.nix`) and run:
   ```sh
   nix flake lock --update-input magpie
   ```
2. Run `bash scripts/prepare.sh` (remove the old `.cache/magpie` first). If patches conflict, resolve against the target tag and regenerate the `.patch` files with `git diff`.
3. Check upstream `internal/redact` (`Options`, `Categories`) and `internal/pii/guard` (`PinnedFiles` / revision) for new exports to wire into `internal/config/` and `internal/engine/`.
4. If `go.mod`/`go.sum` changed, update `vendorHash` in `nix/package.nix`, then run `nix flake check --print-build-logs`.

### Upgrading CPA (`router-for-me/CLIProxyAPI`)

1. Prepare `.cache/magpie`:
   ```sh
   bash scripts/prepare.sh
   ```
2. Update the Go module:
   ```sh
   go get github.com/router-for-me/CLIProxyAPI/v8@<target-tag>
   go mod tidy
   ```
3. Check upstream `sdk/translator` and `internal/constant` for protocol changes (`supportedProtocols` in `internal/config/config.go` and `internal/adapter/cpa.go`), and check `sdk/pluginabi`, `sdk/pluginapi`, and `sdk/cliproxy/executor` for ABI or metadata changes.
4. Update `nixpkgs` if it carries the new `cliproxyapi` (`nix flake lock --update-input nixpkgs`), update `vendorHash` in `nix/package.nix`, and run `nix flake check --print-build-logs`.
