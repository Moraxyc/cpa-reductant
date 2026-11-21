# cpa-reductant

CLIProxyAPI 的原生 `.so` 插件，直接挂载于原版 CPA 进程内，无需改动 CPA 源码或部署独立 Magpie 网关，即可对指定模型做可逆脱敏与响应还原。

本仓库是独立 Go 模块：通过 `patches/magpie/*.patch` 从 `flake.lock` 锁定的 Magpie 源码暴露公开包（`pkg/redact`）与调度钩子，再由 `internal/engine` 直接编排调用。

## 目录结构

- `flake.nix` / `dev/`：Nix Flake（含 `overlay` 导出与基于 `flake-parts` `dev` partition 的 `treefmt`）
- `patches/magpie/`：针对固定版本 Magpie 的最小暴露补丁
- `cmd/plugin/`：CPA C-ABI 桥接与 `-buildmode=c-shared` 入口
- `internal/config/`：插件 YAML 配置解析与模型路由匹配
- `internal/engine/`：基于 Magpie 暴露接口的脱敏/还原调度层
- `internal/adapter/`：CPA 插件协议生命周期、路由与流式转发适配
- `scripts/`：`prepare.sh`（按 `flake.lock` 准备并 patch `.cache/magpie`）与 `build.sh`（编译 `dist/cpa-reductant.so`）
- `test/`：基于 Go `testing` 搭配官方 CLIProxyAPI 二进制与本地 `httptest` mock 的端到端冒烟测试

## 开发与构建

推荐使用 Nix Flake 进入开发环境（会自动挂载打过补丁的 Magpie 到 `.cache/magpie`）：

```sh
nix develop
go test ./...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

也可直接通过 Nix 构建插件包：

```sh
nix build
```

脱离 Nix 环境时，需自行准备 Go 1.26.10+、C 编译器、curl、tar 和 git，`scripts/build.sh` 会依据 `flake.lock` 拉取并打补丁到 `.cache/magpie`，并从 `VERSION` 读取版本号通过 `-ldflags` 注入 `github.com/moraxyc/cpa-reductant/internal/config.Version`，产出 `dist/cpa-reductant.so`。手动调用 `go build` 时也可同样传入 `-ldflags "-X github.com/moraxyc/cpa-reductant/internal/config.Version=$(tr -d '[:space:]' < VERSION)"`。

格式化：

```sh
nix fmt
```

## CPA 配置示例

将 `dist/cpa-reductant.so` 放到 CPA 插件目录，并合并配置：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
      models: [your-existing-claude-model] # 省略或 [] = 所有 Claude 格式模型
      words: [example-private-term]
      block_terms: false # true 时拒绝命中自定义词的请求
```

## 支持范围与限制

目前仅覆盖 Claude `/v1/messages`（JSON 与 SSE）。暂未接入本地 PII 模型，暂不支持其他协议、Home、WebSocket、`count_tokens` 与租户级还原状态隔离；插件未加载或路由回调出错时，CPA 仍可能走默认原生路由。
