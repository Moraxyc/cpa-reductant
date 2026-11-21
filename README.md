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
      models: [your-existing-model] # 省略或 [] = 保护所有已配置的上游 LLM（不是本地 PII 模型开关）
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
      model:                        # 可选：启用本地 PII 识别模型（默认关闭，见下方快速启用指南）
        enabled: false
        auto_download: true         # 启用时默认自动后台拉取权重；设 false 仅限离线手动部署
        name: astrlink-guard        # 目前支持 astrlink-guard
        # dir: /path/to/astrlink-guard # 可选；默认路径规则见下方 model.dir 说明
        min_score: 0.8              # 置信度阈值，范围 [0.5, 1.0)
        strict: false               # false 时未就绪/异常降级为规则脱敏；true 时未就绪/损坏返回 503
        # wait_ms: 2000             # 单次请求等待上限；缺省为非严格 2000ms / 严格 15000ms
        # mirror: https://hf-mirror.com # 可选自定义镜像；留空时 huggingface.co 失败自动回退 hf-mirror.com
```

### 快速启用本地 PII 模型（AstrLink Guard）

> 注意区分：`models`（复数）决定哪些**上游 LLM 请求**走插件脱敏；`model`（单数）才是**本地 PII 语义识别模型**（识别人名、地址、电话等规则难以覆盖的实体）。

1. **在线自动下载（最小配置）**：在 `cpa-reductant` 下只需写以下配置，插件会在后台异步拉取约 39 MB 权重（优先 `huggingface.co`，失败自动回退 `hf-mirror.com`）：

```yaml
model:
  enabled: true
```

2. **`model.dir` 默认路径与查找优先级**：
   - 解析顺序：`model.dir`（YAML） → 环境变量 `CPA_REDUCTANT_MODEL_DIR` → `<UserCacheDir>/cpa-reductant/models/astrlink-guard`
   - 各平台默认缓存路径：
     - **Linux**：`$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard`（未设 `$XDG_CACHE_HOME` 时为 `~/.cache/cpa-reductant/models/astrlink-guard`）
     - **macOS**：`~/Library/Caches/cpa-reductant/models/astrlink-guard`
     - **Windows**：`%LocalAppData%\cpa-reductant\models\astrlink-guard`
   - 若 CPA 以 `systemd` 服务或无 `HOME` 的容器用户运行，建议显式配置 `model.dir` 或 `CPA_REDUCTANT_MODEL_DIR`。
3. **离线手动部署**：从 `QuantumNous/astrlink-guard`（revision `7216b6a3b897ef903d4b1ba367ae6fb95b0fa5e4`）下载 `int8/` 下的以下 3 个文件，直接平铺放到上述目录，并配置 `model: { enabled: true, auto_download: false }`：
   - `model_int8.onnx`（330,781 B）
   - `model_int8.onnx_data`（37,124,608 B）
   - `tokenizer.json`（1,502,881 B）
4. **看板与手动重试下载**：在管理端「插件 → cpa-reductant」看板可直接查看 `model.dir` 解析结果、权重状态与下载进度；若后台拉取失败或为离线配置，可点击「立即下载权重 / 重试下载权重」触发下载。

### 本地 PII 模型特性说明

- **轻量进程内推理**：内置 `astrlink-guard`（约 39 MB int8 量化 BERT），通过纯 Go + SIMD 汇编（macOS 支持 CoreML）在插件进程内运行，无需独立守护进程；支持 4 KB 稳定分块、进程内 LRU 与磁盘特征缓存（`<dir>/found-<rev>.bin`，仅保存哈希与偏移，不落盘明文）。
- **权重目录布局**：自动下载会将权重存放在 `<dir>/<rev[:12]>/` 并在版本升级后自动清理旧版本目录（与 Magpie 缓存结构兼容，可直接指向既有目录复用）；手动离线部署也可将 `model_int8.onnx`、`model_int8.onnx_data`、`tokenizer.json` 直接平铺放在 `<dir>/` 下。
- **超时与降级保护**：按最新消息逆序与最短请求优先调度。非严格模式下超时会保留已扫描分块与规则兜底结果，并在后台完成当前分块写入缓存；严格模式下若模型缺失或损坏会返回 HTTP `503`（不触发 CPA 上游凭据冷却），若持续因新文本超时则临时收敛等待至 `2000ms` 避免长会话卡死。
- **网络与镜像**：后台下载遵循 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量，支持 `Range` 断点续传与 SHA-256 校验，不阻塞 `plugin.register` 与正常请求处理。

### 管理接口与可观测性

插件在 `management.html` 左侧菜单提供了前端入口，并在 `management.secret-key` 保护下提供状态与预览接口。拦截、降级与模型事件也会通过 `host.log` 写入宿主日志。

- **控制面板入口**：`GET /v0/resource/plugins/cpa-reductant/ui` 会显示在 `management.html` 左侧「插件」菜单中。该页面不含任何运行状态或隐私数据；打开后输入 `management.secret-key` 登录，即可加载完整看板。
- **JSON 状态接口**：`GET /v0/management/cpa-reductant/status` 返回全量 JSON 快照，包含插件运行时间、策略与 `key_path` 持久化状态、脱敏与响应/流式还原统计、平均/P95/最大耗时、分协议/模型流量分布、本地 PII 模型状态及近期非明文审计事件环（支持 `?events=N` 调节条数、`?action=mask|warn|block|restore|passthrough|error` 过滤）。
- **HTML 监控看板**：`GET /v0/management/cpa-reductant/ui`（或 `GET /v0/management/cpa-reductant/status?format=html` / `Accept: text/html`）使用服务端渲染单页 HTML 展示过滤器运行状态、敏感类别与检出引擎分布条、协议/模型处理量、分色过滤流水线（仅展示前缀 Hint 与占位符 ID）、调试抓包面板与在线只读预览区（支持 `?refresh=5` 开启自动刷新）。
- **临时调试抓包**：`GET/POST/DELETE /v0/management/cpa-reductant/debug/capture` 提供仅内存、带 TTL 自动过期（默认 5 分钟、上限 30 分钟）的明文调试抓包开关。`POST` 传入 `{"enabled": true, "ttl_seconds": 300}` 开启后，可通过 `GET`（支持 `?id=`、`?limit=`、`?protocol=`、`?model=`）或看板直接查看原始请求、脱敏后请求、上游原始响应、还原后响应与逐项替换对照；`DELETE` 一键清空缓存。
- **本地模型下载**：`POST /v0/management/cpa-reductant/model/download` 可手动触发或重试后台权重下载（支持传入 `{"mirror": "..."}` 覆盖镜像）。
- **只读策略预览**：`POST /v0/management/cpa-reductant/preview` 接收 `{"text": "..."}`，以当前策略执行只读脱敏预览（不写入占位符记忆与统计），返回预览文本、命中元数据与耗时。

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
