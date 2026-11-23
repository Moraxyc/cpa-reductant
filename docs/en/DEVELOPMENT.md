**English** | [简体中文](../zh/DEVELOPMENT.md)

# Developer Guide

This guide is for contributors and developers working on `cpa-reductant`. It covers the repository architecture, Magpie patch mechanism, source builds, testing, and upstream upgrade workflows. If you only want to install and use the plugin with CLIProxyAPI, return to the [README](../../README.md).

## Architecture Overview

`cpa-reductant` is a standalone Go module compiled into a native shared library via C-ABI (`-buildmode=c-shared`) and loaded by the CLIProxyAPI host:

1. **Upstream exposure (`patches/magpie/`)**: `flake.lock` pins the exact upstream `yetone/magpie` revision. Minimal patches (`0001-expose-redact.patch`, `0002-expose-guard.patch`) expose Magpie's internal `redact` and `guard` packages as `pkg/redact` and `pkg/guard` along with scheduling hooks, placed in `.cache/magpie/` and referenced via `replace` in `go.mod`.
2. **Engine layer (`internal/engine/`)**: wraps Magpie's rule/wordlist redaction and response restoration, and manages AstrLink Guard local PII inference scheduling (4 KB stable chunking, in-memory LRU, disk feature cache), background resumable model downloads, telemetry counters, non-plaintext audit event ring, and temporary debug captures.
3. **Host adapter (`internal/adapter/` & `cmd/plugin/`)**: `cmd/plugin` provides the CPA C-ABI exported symbols and host RPC glue; `internal/adapter` integrates with the CPA plugin lifecycle (`register` / `reconfigure` / `route` / `execute` / `execute_stream` / `management`), normalized SSE streaming forwarding, and the management UI.

## Repository Layout

- `cmd/plugin/`: CPA C-ABI bridge (`abi.h`, `abi.c`) and `-buildmode=c-shared` entry point (`main.go`)
- `internal/config/`: plugin YAML config parsing, validation, default policies, and protocol/model route matching
- `internal/engine/`: redaction/restoration orchestrator, local PII inference queue and cache, weight downloader, observability metrics, and debug capture
- `internal/adapter/`: CPA plugin protocol adapter, SSE streaming forwarder, and server-rendered management dashboard
- `patches/magpie/`: minimal exposure patches targeting the pinned Magpie version
- `scripts/`: `prepare.sh` (fetches and patches `.cache/magpie` per `flake.lock`) and `build.sh` (compiles `dist/cpa-reductant.so`)
- `test/`: end-to-end smoke tests using Go `testing`, the official `cliproxyapi` binary, and local `httptest` mocks
- `flake.nix` / `dev/` / `nix/`: Nix Flake builds, `treefmt` formatting, portable cross-compilation (Zig CC), and `smoke` checks
- `.github/workflows/`: `ci.yml`, `release.yml`, and `upstream-watch.yml`

## Development & Building

### Option 1: Nix Flake (Recommended)

Entering the dev shell automatically links the Nix-patched Magpie tree to `.cache/magpie` via `shellHook`:

```sh
nix develop --accept-flake-config
go test -race ./...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

Build and verify directly with Nix:

```sh
# Build plugin package for current platform
nix build --accept-flake-config

# Run full Flake checks (unit tests, CPA end-to-end smoke tests, portable cross builds)
nix flake check --print-build-logs

# Format Go and Nix files (gofmt + nixfmt)
nix fmt
```

Cross-compile portable binaries from a Linux host using Zig CC:

```sh
nix build .#portable-linux-amd64
nix build .#portable-linux-arm64
nix build .#portable-windows-amd64
nix build .#portable-windows-arm64
```

To configure the project's Nix binary cache manually:

- Substituter: `https://nix-cache.qaq.li/cpa-reductant`
- Trusted Public Key: `cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo=`

### Option 2: Building Without Nix

Ensure the following tools are available:

- Go `1.26.10+` (matching `go.mod`)
- C compiler (`gcc` or `clang`, for CGO and `cmd/plugin/abi.c`)
- `curl`, `tar`, `git` (optional: `jq`)

Run the build script:

```sh
bash scripts/build.sh
```

`scripts/build.sh` invokes `scripts/prepare.sh` to fetch the Magpie commit pinned in `flake.lock` and apply `patches/magpie/*.patch` into `.cache/magpie`, then injects `VERSION` via `-ldflags` into `github.com/moraxyc/cpa-reductant/internal/config.Version` and outputs `dist/cpa-reductant.so`.

To prepare `.cache/magpie` and run `go build` manually:

```sh
bash scripts/prepare.sh
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$(tr -d '[:space:]' < VERSION)" \
  -o dist/cpa-reductant.so ./cmd/plugin
```

`scripts/prepare.sh` also supports two environment variables for reusing local sources:

- `MAGPIE_PATCHED_SRC`: symlink directly to an already-patched Magpie directory
- `MAGPIE_SOURCE_CACHE`: use a local unpatched Magpie source checkout instead of downloading

## Testing

```sh
# Unit tests (with race detector)
go test -race ./internal/...

# End-to-end smoke tests (requires official cliproxyapi binary)
CPA_BIN=/path/to/cliproxyapi go test -v ./test/...
```

`test/smoke_test.go` accepts `CPA_PLUGIN_PATH` to test a prebuilt shared library; if unset, it builds the plugin library automatically during the test run.

## CI & Release Workflows

- **CI (`.github/workflows/ci.yml`)**: runs `nix flake check`, Linux/Windows portable cross-builds, non-Nix `scripts/build.sh` + `go vet` verification, and macOS (`darwin/amd64`, `darwin/arm64`) unit tests and `.dylib` builds on `main` and pull requests.
- **Upstream Watch (`.github/workflows/upstream-watch.yml`)**: runs every Monday and Thursday (or via `workflow_dispatch`) to check for new `yetone/magpie` and `router-for-me/CLIProxyAPI` releases, dry-run `patches/magpie/*.patch`, and run `nix flake check` against the latest `nixpkgs` `cliproxyapi`. Creates or updates a tracking issue labeled `upstream-sync` when action is needed and closes it automatically once aligned.
- **Release (`.github/workflows/release.yml`)**: triggered when pushing a `v*` tag (matching the root `VERSION` file). Builds all 6 target artifacts via Nix (Linux/Windows amd64 & arm64) and macOS runners (darwin amd64 & arm64), packages them as `cpa-reductant_<version>_<os>_<arch>.zip`, generates `checksums.txt`, and publishes to GitHub Releases.

## Upstream Sync & Upgrade Checklist

When an `upstream-sync` tracking issue is opened, follow the corresponding checklist below:

### Upgrading Magpie (`yetone/magpie`)

1. **Update the Flake input**: bump the tag in `inputs.magpie.url` in `flake.nix`, then refresh `flake.lock`:
   ```sh
   nix flake lock --update-input magpie
   ```
2. **Verify and refresh patches**: run `bash scripts/prepare.sh` (remove or overwrite the old `.cache/magpie` first):
   - If `patches/magpie/*.patch` conflicts, resolve the changes in a temporary clone of the target tag and regenerate the corresponding `.patch` file via `git diff`.
   - Check upstream `internal/redact` (`Options`, `Categories`) and `internal/pii/guard` (`PinnedFiles` / model revision) for any new symbols to expose, and wire them up in `internal/config/config.go` and `internal/engine/`.
3. **Update `vendorHash` and verify**: if `go.mod` or `go.sum` changed, update `vendorHash` in `nix/package.nix`, then run:
   ```sh
   nix flake check --print-build-logs
   ```

### Upgrading CPA (`router-for-me/CLIProxyAPI`)

1. **Prepare local Magpie replacement** (`go.mod` points to `./.cache/magpie`):
   ```sh
   bash scripts/prepare.sh
   ```
2. **Upgrade the Go dependency**:
   ```sh
   go get github.com/router-for-me/CLIProxyAPI/v8@<target-tag>
   go mod tidy
   ```
3. **Check protocol and ABI changes**:
   - Inspect upstream `sdk/translator` and `internal/constant` for added or renamed protocols, and sync `supportedProtocols` in `internal/config/config.go` and `internal/adapter/cpa.go`.
   - Check `sdk/pluginabi`, `sdk/pluginapi`, and `sdk/cliproxy/executor` for struct/method or `RequestPathMetadataKey` changes.
   - If `nixpkgs` already includes the newer `cliproxyapi`, update it as well with `nix flake lock --update-input nixpkgs`.
4. **Update `vendorHash` and verify**: update `vendorHash` in `nix/package.nix`, then run full checks:
   ```sh
   nix flake check --print-build-logs
   ```
