[English](../en/DEVELOPMENT.md) | **简体中文**

# 开发者指南

## 结构与分工

`cpa-reductant` 编译为 Go C-ABI 动态库（`-buildmode=c-shared`）供 CLIProxyAPI 加载：

- `patches/magpie/` — 面向 `flake.lock` 锁定版本的 `yetone/magpie` 补丁（`0001-expose-redact.patch`、`0002-expose-guard.patch`），将内部包导出为 `pkg/redact` 与 `pkg/guard` 放到 `.cache/magpie/`（由 `go.mod` 的 `replace` 引入）。
- `cmd/plugin/` — CPA C-ABI 桥接（`abi.h`、`abi.c`）与 `c-shared` 入口（`main.go`）。
- `internal/config/` — YAML 解析、校验、策略合并与模型/协议路由。
- `internal/engine/` — 脱敏/还原调度、本地与 HTTP PII 推理缓存、`CallerScope` 隔离、Webhook 告警、Prometheus 指标、权重下载与调试抓包。
- `internal/adapter/` — CPA 插件生命周期适配（`register`、`reconfigure`、`route`、`execute`、`execute_stream`、`management`）、SSE 流规范化与管理面板。
- `scripts/` — `prepare.sh`（拉取并 patch `.cache/magpie`）与 `build.sh`（产出 `dist/cpa-reductant.so`）。
- `test/` — 基于官方 `cliproxyapi` 与本地 `httptest` mock 的端到端冒烟测试。
- `flake.nix`、`dev/`、`nix/` — Nix 构建、`treefmt`、Zig CC 交叉编译与 `smoke` 检查。
- `.github/workflows/` — `ci.yml`、`release.yml`、`upstream-watch.yml`。

## 构建

### 使用 Nix

进入 `nix develop` 时，`shellHook` 会将已打补丁的 Magpie 软链到 `.cache/magpie`：

```sh
nix develop --accept-flake-config
go test -race ./internal/...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

常用 Nix 命令：

```sh
nix build --accept-flake-config         # 构建当前平台插件
nix flake check --print-build-logs      # 单测、端到端冒烟与交叉构建检查
nix fmt                                 # gofmt + nixfmt
```

在 Linux 上通过 Zig CC 交叉构建便携产物：

```sh
nix build .#portable-linux-amd64
nix build .#portable-linux-arm64
nix build .#portable-windows-amd64
nix build .#portable-windows-arm64
```

二进制缓存：

- Substituter: `https://nix-cache.qaq.li/cpa-reductant`
- Trusted Public Key: `cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo=`

### 脱离 Nix 构建

需要 Go `1.26.10+`、C 编译器（`gcc`/`clang`）、`curl`、`tar`、`git`（可选 `jq`）。

```sh
bash scripts/build.sh
```

`build.sh` 调用 `scripts/prepare.sh` 按 `flake.lock` 拉取并 patch `.cache/magpie`，把 `VERSION` 注入 `internal/config.Version`，生成 `dist/cpa-reductant.so`。

手动分步构建：

```sh
bash scripts/prepare.sh
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$(tr -d '[:space:]' < VERSION)" \
  -o dist/cpa-reductant.so ./cmd/plugin
```

`prepare.sh` 支持的环境变量：

- `MAGPIE_PATCHED_SRC` — 直接软链本地已打好补丁的 Magpie 目录
- `MAGPIE_SOURCE_CACHE` — 使用本地未打补丁的 Magpie 源码目录代替下载

## 测试与基准

```sh
go test -race ./internal/...
go test -run='^$' -bench=. -benchmem ./internal/engine ./internal/adapter
CPA_BIN=/path/to/cliproxyapi go test -v ./test/...
```

`test/smoke_test.go` 支持用 `CPA_PLUGIN_PATH` 指定已编好的动态库；未指定时会在测试中即时编译。

## CI 与发布

- `ci.yml` — 运行 `nix flake check`、Linux/Windows 交叉构建、脱离 Nix 的 `scripts/build.sh` + `go vet`，以及 macOS（`amd64`/`arm64`）单测与 `.dylib` 构建。
- `upstream-watch.yml` — 每周一、周四（或手动触发）检查 `yetone/magpie` 与 `router-for-me/CLIProxyAPI` 新版本、验证 `patches/magpie/*.patch`，并用最新 `nixpkgs` 的 `cliproxyapi` 试跑 `nix flake check`。需要适配时自动创建/更新 `upstream-sync` Issue，对齐后关闭。
- `release.yml` — 推送 `v*` 标签（需与根目录 `VERSION` 一致）触发，构建 6 个平台产物（`cpa-reductant_<version>_<os>_<arch>.zip`）和 `checksums.txt` 并发布到 GitHub Releases。

## 上游升级

### 升级 Magpie（`yetone/magpie`）

1. 更新 `flake.nix` 中 `inputs.magpie.url` 的 tag，刷新锁文件：
   ```sh
   nix flake lock --update-input magpie
   ```
2. 删除旧 `.cache/magpie` 后执行 `bash scripts/prepare.sh`。如补丁冲突，在对应 tag 下解决后用 `git diff` 重新生成 `.patch`。
3. 检查上游 `internal/redact`（`Options`、`Categories`）与 `internal/pii/guard`（`PinnedFiles` / revision）的新导出，同步到 `internal/config/` 与 `internal/engine/`。
4. 如 `go.mod`/`go.sum` 变动，更新 `nix/package.nix` 中的 `vendorHash`，运行 `nix flake check --print-build-logs`。

### 升级 CPA（`router-for-me/CLIProxyAPI`）

1. 先准备 `.cache/magpie`：
   ```sh
   bash scripts/prepare.sh
   ```
2. 升级依赖：
   ```sh
   go get github.com/router-for-me/CLIProxyAPI/v8@<目标tag>
   go mod tidy
   ```
3. 核对上游 `sdk/translator`、`internal/constant` 的协议变更（同步到 `internal/config/config.go` 和 `internal/adapter/cpa.go`），以及 `sdk/pluginabi`、`sdk/pluginapi`、`sdk/cliproxy/executor` 的 ABI 或 metadata 变动。
4. 如 `nixpkgs` 已同步新版 `cliproxyapi`，执行 `nix flake lock --update-input nixpkgs`，更新 `nix/package.nix` 中的 `vendorHash`，再跑 `nix flake check --print-build-logs`。
