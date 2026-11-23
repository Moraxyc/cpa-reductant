[English](../en/DEVELOPMENT.md) | **简体中文**

# 开发者指南

本文档面向 `cpa-reductant` 的贡献者与二次开发者，介绍项目架构、Magpie 补丁机制、源码构建与测试流程。如果你只是想在 CLIProxyAPI 中安装和使用本插件，请返回 [README](README.md)。

## 架构与工作原理

`cpa-reductant` 是一个独立 Go 模块，通过 C-ABI（`-buildmode=c-shared`）编译为原生动态库供 CLIProxyAPI 宿主加载：

1. **依赖暴露（`patches/magpie/`）**：`flake.lock` 锁定上游 `yetone/magpie` 的具体版本；仓库通过最小补丁（`0001-expose-redact.patch`、`0002-expose-guard.patch`）将 Magpie 内部的 `redact` 与 `guard` 能力暴露为 `pkg/redact`、`pkg/guard` 公开包与调度钩子，放在 `.cache/magpie/` 中供 `go.mod` 的 `replace` 引用。
2. **调度引擎（`internal/engine/`）**：封装 Magpie 的规则/词表脱敏与响应还原逻辑，并负责 AstrLink Guard 本地 PII 推理调度（4 KB 稳定分块、进程内 LRU、磁盘特征缓存）、后台断点续传下载、统计指标、非明文审计事件环与临时调试抓包。
3. **宿主适配（`internal/adapter/` & `cmd/plugin/`）**：`cmd/plugin` 实现 CPA C-ABI 导出符号与宿主 RPC 胶水；`internal/adapter` 对接 CPA 插件生命周期（`register` / `reconfigure` / `route` / `execute` / `execute_stream` / `management`）、流式 SSE 规范化转发及管理端 UI。

## 目录结构

- `cmd/plugin/`：CPA C-ABI 桥接（`abi.h`、`abi.c`）与 `-buildmode=c-shared` 入口（`main.go`）
- `internal/config/`：插件 YAML 配置解析、校验、默认策略与协议/模型路由匹配
- `internal/engine/`：脱敏/还原调度层、本地 PII 推理队列与缓存、权重下载器、可观测性统计与调试抓包
- `internal/adapter/`：CPA 插件协议适配、SSE 流式转发与服务端渲染管理看板
- `patches/magpie/`：针对固定版本 Magpie 的最小暴露补丁
- `scripts/`：`prepare.sh`（按 `flake.lock` 准备并 patch `.cache/magpie`）与 `build.sh`（编译 `dist/cpa-reductant.so`）
- `test/`：基于 Go `testing`、官方 `cliproxyapi` 二进制与本地 `httptest` mock 的端到端冒烟测试
- `flake.nix` / `dev/` / `nix/`：Nix Flake 构建、`treefmt` 格式化、跨平台 portable 交叉编译（Zig CC）与 `smoke` 检查
- `.github/workflows/`：`ci.yml` 与 `release.yml`

## 开发与构建

### 方式一：Nix Flake（推荐）

进入开发环境时，`shellHook` 会自动将 Nix 打好补丁的 Magpie 链接到 `.cache/magpie`：

```sh
nix develop --accept-flake-config
go test -race ./...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

直接通过 Nix 构建与验证：

```sh
# 构建当前平台插件包
nix build --accept-flake-config

# 运行完整 Flake 检查（含单测、CPA 端到端冒烟测试与 portable 交叉构建）
nix flake check --print-build-logs

# 格式化 Go 与 Nix 代码（gofmt + nixfmt）
nix fmt
```

在 Linux 主机上还可以通过 Zig CC 交叉构建便携产物：

```sh
nix build .#portable-linux-amd64
nix build .#portable-linux-arm64
nix build .#portable-windows-amd64
nix build .#portable-windows-arm64
```

如需手动配置项目的 Nix 二进制缓存：

- Substituter: `https://nix-cache.qaq.li/cpa-reductant`
- Trusted Public Key: `cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo=`

### 方式二：脱离 Nix 环境构建

需自行准备以下工具：

- Go `1.26.10+`（与 `go.mod` 一致）
- C 编译器（`gcc` 或 `clang`，用于 CGO 与 `cmd/plugin/abi.c`）
- `curl`、`tar`、`git`（可选 `jq`）

运行构建脚本：

```sh
bash scripts/build.sh
```

`scripts/build.sh` 会自动调用 `scripts/prepare.sh`，依据 `flake.lock` 下载对应 commit 的 Magpie 源码并应用 `patches/magpie/*.patch` 到 `.cache/magpie`，随后从 `VERSION` 读取版本号通过 `-ldflags` 注入 `github.com/moraxyc/cpa-reductant/internal/config.Version`，产出 `dist/cpa-reductant.so`。

若只准备 `.cache/magpie` 后手动调用 `go build`：

```sh
bash scripts/prepare.sh
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$(tr -d '[:space:]' < VERSION)" \
  -o dist/cpa-reductant.so ./cmd/plugin
```

`scripts/prepare.sh` 也支持两个环境变量以复用本地源码：

- `MAGPIE_PATCHED_SRC`：直接软链已打好补丁的 Magpie 目录
- `MAGPIE_SOURCE_CACHE`：使用本地未打补丁的 Magpie 源码目录代替网络拉取

## 测试

```sh
# 单元测试（包含竞态检测）
go test -race ./internal/...

# 端到端冒烟测试（需要官方 cliproxyapi 二进制）
CPA_BIN=/path/to/cliproxyapi go test -v ./test/...
```

`test/smoke_test.go` 支持通过 `CPA_PLUGIN_PATH` 指定已构建好的动态库路径；未设置时会自动在测试过程中编译一次插件动态库。

## CI 与发布流程

- **CI（`.github/workflows/ci.yml`）**：在 `main` 分支与 PR 上运行 `nix flake check`、Linux/Windows portable 交叉编译、脱离 Nix 的 `scripts/build.sh` 与 `go vet` 检查，以及 macOS（`darwin/amd64`、`darwin/arm64`）单测与动态库构建验证。
- **Upstream Watch（`.github/workflows/upstream-watch.yml`）**：每周一、周四定时（或手动 `workflow_dispatch`）检查 `yetone/magpie` 与 `router-for-me/CLIProxyAPI` 新版本、试应用 `patches/magpie/*.patch`，并用最新 `nixpkgs` 的 `cliproxyapi` 试跑 `nix flake check`；发现需要适配时自动创建/更新带 `upstream-sync` 标签的跟踪 Issue，全部对齐后自动关闭。
- **Release（`.github/workflows/release.yml`）**：推送 `v*` 标签（需与根目录 `VERSION` 文件一致）时触发，分别通过 Nix（Linux/Windows amd64 & arm64）与 macOS runner（darwin amd64 & arm64）构建 6 个平台产物，打包为 `cpa-reductant_<version>_<os>_<arch>.zip` 并生成 `checksums.txt` 发布到 GitHub Releases。

## 上游适配与升级流程

收到 `upstream-sync` Issue 提醒后，按以下对应清单升级并验证即可：

### 升级 Magpie（`yetone/magpie`）

1. **更新 Flake 输入**：将 `flake.nix` 中 `inputs.magpie.url` 的 tag 更新为目标版本，然后刷新锁文件：
   ```sh
   nix flake lock --update-input magpie
   ```
2. **验证与刷新补丁**：运行 `bash scripts/prepare.sh`（需先清理旧 `.cache/magpie` 或直接覆盖）：
   - 若 `patches/magpie/*.patch` 出现冲突，在临时克隆的对应 tag 目录中手动调整并重新生成 `git diff` 覆盖对应 `.patch` 文件；
   - 核对上游 `internal/redact`（`Options`、`Categories`）与 `internal/pii/guard`（`PinnedFiles` / 模型 revision）是否有新增导出需求，并在 `internal/config/config.go`、`internal/engine/` 中同步接入。
3. **更新 `vendorHash` 与验证**：若 `go.mod` / `go.sum` 有变化，更新 `nix/package.nix` 中的 `vendorHash`，再运行：
   ```sh
   nix flake check --print-build-logs
   ```

### 升级 CPA（`router-for-me/CLIProxyAPI`）

1. **准备本地 Magpie 替换目录**（`go.mod` 依赖 `./.cache/magpie`）：
   ```sh
   bash scripts/prepare.sh
   ```
2. **升级 Go 依赖**：
   ```sh
   go get github.com/router-for-me/CLIProxyAPI/v8@<目标tag>
   go mod tidy
   ```
3. **核对协议与 ABI 适配点**：
   - 核对上游 `sdk/translator` 与 `internal/constant` 是否新增或重命名协议，同步更新 `internal/config/config.go` 的 `supportedProtocols` 及 `internal/adapter/cpa.go`；
   - 核对 `sdk/pluginabi`、`sdk/pluginapi` 与 `sdk/cliproxy/executor` 是否有字段/方法或 `RequestPathMetadataKey` 变更；
   - 如 `nixpkgs` 已同步新版 `cliproxyapi`，顺手执行 `nix flake lock --update-input nixpkgs`。
4. **更新 `vendorHash` 与验证**：同步更新 `nix/package.nix` 中的 `vendorHash`，运行完整检查：
   ```sh
   nix flake check --print-build-logs
   ```
