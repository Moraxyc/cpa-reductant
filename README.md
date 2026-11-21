# cpa-reductant

CLIProxyAPI 的原生 `.so` 插件，直接挂载于原版 CPA 进程内，无需改动 CPA 源码或部署独立 Magpie 网关，即可对指定模型做可逆脱敏与响应还原。

本仓库是独立 Go 模块：通过 `patches/magpie/*.patch` 从 `flake.lock` 锁定的 Magpie 源码暴露公开包（`pkg/redact`、`pkg/guard`）与调度钩子，再由 `internal/engine` 直接编排调用。

## 目录结构

- `flake.nix` / `dev/`：Nix Flake（含 `overlay` 导出与基于 `flake-parts` `dev` partition 的 `treefmt`）
- `patches/magpie/`：针对固定版本 Magpie 的最小暴露补丁
- `cmd/plugin/`：CPA C-ABI 桥接与 `-buildmode=c-shared` 入口
- `internal/config/`：插件 YAML 配置解析与模型路由匹配
- `internal/engine/`：基于 Magpie 暴露接口的脱敏/还原调度层（规则/词表脱敏、AstrLink Guard 本地 PII 推理、分块/磁盘特征缓存与后台断点下载）
- `internal/adapter/`：CPA 插件协议生命周期、路由与流式转发适配
- `scripts/`：`prepare.sh`（按 `flake.lock` 准备并 patch `.cache/magpie`）与 `build.sh`（编译 `dist/cpa-reductant.so`）
- `test/`：基于 Go `testing` 搭配官方 CLIProxyAPI 二进制与本地 `httptest` mock 的端到端冒烟测试

## 开发与构建

推荐使用 Nix Flake 进入开发环境（会自动挂载打过补丁的 Magpie 到 `.cache/magpie`）：

```sh
nix develop --accept-flake-config
go test ./...
bash scripts/build.sh
CPA_BIN=cliproxyapi go test -v ./test/...
```

也可直接通过 Nix 构建插件包：

```sh
nix build --accept-flake-config
```

如需手动配置 Nix 二进制缓存：

- Substituter: `https://nix-cache.qaq.li/cpa-reductant`
- Trusted Public Key: `cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo=`

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
      models: [your-existing-model] # 省略或 [] = 所有已配置模型
      protocols: []                 # 省略或 [] = 所有已支持的 CPA 通讯格式
      secrets: true                 # 默认 true：脱敏密钥/Token/私钥/密码
      personal: true                # 默认 true：脱敏内建个人隐私类别
      kinds:                        # 可选：按类别覆盖个人隐私开关（home/userhost/ip/bucket 默认关闭）
        ip: false
        home: false
      words: [example-private-term]
      words_any_case: false         # true 时忽略大小写匹配 words
      words_whole: false            # true 时仅匹配完整词边界
      # rules:                      # 可选：自定义前缀或正则脱敏规则（最多 32 条）
      #   - kind: CORP_KEY
      #     prefix: crp_
      #   - kind: TICKET
      #     regex: 'JIRA-\d{4,}'
      # actions:                    # 可选：按占位符类型覆盖动作（mask | warn | block）
      #   EMAIL: warn
      #   PRIVATE_KEY: block
      # allow: [ops@example.org]    # 可选：允许明文放行的精确值（仍受 words 优先保护）
      shape: false                  # true 时使用保形伪造值替代 {{...}} 占位符
      block_terms: false            # true 时拒绝命中自定义词的请求（等价 actions.TERM=block）
      # key_path: /var/lib/cpa/reductant.key # 可选（或 CPA_REDUCTANT_KEY_PATH）：跨重启持久化占位符 HMAC 密钥
      model:             # 可选：启用本地 PII 识别模型（默认关闭）
      model:             # 可选：启用本地 PII 识别模型（默认关闭）
        enabled: false
        name: astrlink-guard # 目前支持 astrlink-guard
        # dir: /path/to/astrlink-guard # 可选；默认读取 CPA_REDUCTANT_MODEL_DIR 或 <UserCacheDir>/cpa-reductant/models/astrlink-guard
        min_score: 0.8       # 置信度阈值，范围 [0.5, 1.0)
        strict: false        # false 时未就绪/异常降级为规则脱敏；true 时未就绪/损坏返回 503
        # wait_ms: 2000      # 单次请求等待上限；缺省为非严格 2000ms / 严格 15000ms
        auto_download: false # true 时后台异步拉取权重并按 SHA-256 校验（默认不主动联网）
        # mirror: https://hf-mirror.com # 可选自定义镜像；留空时 huggingface.co 失败自动回退 hf-mirror.com
```

### 本地 PII 模型说明（AstrLink Guard）

- **轻量进程内推理**：内置 `astrlink-guard`（约 39 MB int8 量化 BERT），通过纯 Go + SIMD 汇编（macOS 支持 CoreML）在插件进程内运行，无需独立守护进程；支持 4 KB 稳定分块、进程内 LRU 与磁盘特征缓存（`<dir>/found-<rev>.bin`，仅保存哈希与偏移，不落盘明文）。
- **权重目录布局**：`auto_download: true` 会将权重下载到 `<dir>/<rev[:12]>/` 并在版本升级后自动清理旧版本目录（与 Magpie 缓存结构兼容，可直接指向既有目录复用）；手动离线部署也可将 `model_int8.onnx`、`model_int8.onnx_data`、`tokenizer.json` 直接平铺放在 `<dir>/` 下。
- **超时与降级保护**：按最新消息逆序与最短请求优先调度。非严格模式下超时会保留已扫描分块与规则兜底结果，并在后台完成当前分块写入缓存；严格模式下若模型缺失或损坏会返回 HTTP `503`（不触发 CPA 上游凭据冷却），若持续因新文本超时则临时收敛等待至 `2000ms` 避免长会话卡死。
- **网络与镜像**：后台下载遵循 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量，支持 `Range` 断点续传与 SHA-256 校验，不阻塞 `plugin.register` 与正常请求处理。

### 管理接口与可观测性

插件向 CPA 注册了两条认证 Management API 路由（受 CPA `management.secret-key` 保护）：

- `GET /v0/management/cpa-reductant/status`：返回插件版本、当前生效策略摘要（不含敏感词/白名单明文）、脱敏命中统计及本地 PII 模型加载/下载/缓存状态。
- `POST /v0/management/cpa-reductant/preview`：接收 `{"text": "..."}`，以当前策略执行只读脱敏预览（不写入占位符记忆与统计），返回预览文本与命中元数据。

## 支持范围与限制

已支持 CPA 的全部主流通讯格式（JSON 与流式 SSE）：

- `claude`：`/v1/messages`（`/v1/messages/count_tokens` 透传原生计数）
- `openai`：`/v1/chat/completions`、`/v1/completions`
- `openai-response`：`/v1/responses`、`/v1/responses/compact`、`/backend-api/codex/responses`（支持 WebSocket `/v1/responses` 客户端接入，上游转发会回退为 HTTP）
- `gemini`：`/v1beta/models/*:generateContent`、`:streamGenerateContent`
- `interactions`：`/v1beta/interactions`（`model` 请求；`agent` 原生请求透传）
- `openai-image` / `openai-speech` / `openai-video`：文生图/改图、TTS（保留二进制音频响应）、视频生成
- `codex` / `antigravity`：SDK 内部格式

当前限制：

- 本地 PII 模型当前仅支持 `astrlink-guard`（CPU / macOS CoreML），暂未接入依赖独立 `pf-serve` 进程的 `privacy-filter` 与 Linux Vulkan/GPU 加速。
- 暂不支持 CPA Home 模式与 `/v1/alpha/search`（CPA 原生限制）。
- `count_tokens` 与 `interactions` `agent` 请求会直接透传原生处理，不做脱敏。
- 占位符还原映射在进程内共享，暂未按租户隔离；插件未加载或路由失败时，CPA 会回退原生路由直出。
