# CPA Reductant

CLIProxyAPI（CPA）的隐私脱敏插件：将插件放入 CPA 插件目录并启用后，发往大模型的密钥、个人隐私和自定义敏感词会自动替换为占位符，模型回复时再自动还原回原文（同时支持流式输出）。

> **读者导航**
> - **只是想安装和使用插件**：直接阅读本文即可。
> - **想了解代码结构、修改 Magpie 补丁或从源码编译测试**：请阅读 [开发者指南（docs/DEVELOPMENT.md）](docs/DEVELOPMENT.md)。

---

## 安装插件

### 方式一：下载预编译产物（推荐）

前往 [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) 下载对应系统的压缩包（可配合最新版本的 [checksums.txt](https://github.com/Moraxyc/cpa-reductant/releases/latest/download/checksums.txt) 校验 SHA-256）：

| 系统 / 架构 | 压缩包文件名 | 解压得到的插件文件 | 获取方式 |
| --- | --- | --- | --- |
| Linux x86_64 | `cpa-reductant_<version>_linux_amd64.zip` | `cpa-reductant.so` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |
| Linux ARM64 | `cpa-reductant_<version>_linux_arm64.zip` | `cpa-reductant.so` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |
| macOS Intel | `cpa-reductant_<version>_darwin_amd64.zip` | `cpa-reductant.dylib` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |
| macOS Apple Silicon | `cpa-reductant_<version>_darwin_arm64.zip` | `cpa-reductant.dylib` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |
| Windows x86_64 | `cpa-reductant_<version>_windows_amd64.zip` | `cpa-reductant.dll` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |
| Windows ARM64 | `cpa-reductant_<version>_windows_arm64.zip` | `cpa-reductant.dll` | [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) |

将解压后的插件文件放入 CPA 配置中 `plugins.dir` 指定的目录（例如与配置文件同级的 `plugins/` 目录）。

### 方式二：通过 Nix 安装

如果使用 Nix / NixOS 管理 CLIProxyAPI，本仓库 Flake 导出了 `packages.<system>.cpa-reductant` 以及 `overlays.default`（提供 `pkgs.cpa-reductant`），构建产物位于：

```text
$out/lib/cliproxyapi/plugins/<os>/<arch>/cpa-reductant.<ext>
```

---

## 快速开始

在 CLIProxyAPI 的 `config.yaml` 中开启插件系统并启用 `cpa-reductant`：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
```

启动或重载 CPA 后即生效。默认配置下：

- **生效范围**：自动保护**所有**已配置的上游模型及已支持的协议格式；
- **默认脱敏规则**：自动脱敏常见密钥（API Key、私钥、Token、密码等）与个人隐私（邮箱、电话、银行卡号、证件号等）；
- **默认安全策略**：`home`、`userhost`、`ip`、`bucket` 默认关闭，避免误伤编程场景中的系统路径与常见网络地址。

---

## 完整配置说明

下面列出 `plugins.configs.cpa-reductant` 支持的全部配置项与默认值：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true

      # 1. 作用范围控制
      models: []                    # 目标上游 LLM 模型 ID；省略或 [] 表示保护所有已配置模型（注意与下方 model 区分）
      protocols: []                 # 限定生效的 CPA 协议格式；省略或 [] 表示全部支持格式

      # 2. 内建规则开关
      secrets: true                 # 默认 true：脱敏密钥、API Token、私钥、密码等
      personal: true                # 默认 true：脱敏内建个人隐私（邮箱、电话、证件、银行卡等）
      kinds:                        # 可选：按具体个人隐私类别覆盖默认开关
        ssn: true                   # 默认开启：ssn, passport, iban, birthday, mac, serial
        home: false                 # 默认关闭：home, userhost, ip, bucket
        ip: false

      # 3. 自定义词表与正则规则
      words: [example-private-term] # 需要保护的自定义敏感词（字面量）
      words_any_case: false         # true 时忽略大小写匹配 words
      words_whole: false            # true 时仅在完整词边界匹配 words
      allow: [ops@example.org]      # 允许明文放行的精确值（若同时在 words 中，仍优先受 words 保护）
      # rules:                      # 自定义前缀或正则规则（最多 32 条；prefix 与 regex 二选一）
      #   - kind: CORP_KEY
      #     prefix: crp_
      #   - kind: TICKET
      #     regex: 'JIRA-\d{4,}'

      # 4. 命中动作与占位符行为
      # actions:                    # 按占位符类型覆盖命中动作：mask（脱敏）| warn（仅告警）| block（拒绝请求并返回 400）
      #   EMAIL: warn
      #   PRIVATE_KEY: block
      block_terms: false            # true 时拒绝命中 words 自定义词的请求（等价 actions.TERM=block）
      shape: false                  # true 时使用保形伪造值替代 {{...}} 占位符
      # key_path: /var/lib/cpa/reductant.key # 可选（或环境变量 CPA_REDUCTANT_KEY_PATH）：持久化占位符 HMAC 密钥

      # 5. 本地 PII 语义识别模型（默认关闭，见下一节）
      model:
        enabled: false
        auto_download: true
        name: astrlink-guard
        # dir: /path/to/astrlink-guard
        min_score: 0.8
        strict: false
        # wait_ms: 2000
        # mirror: https://hf-mirror.com
```

### 配置要点说明

- **`models`（复数）vs `model`（单数）**：`models` 决定哪些**上游大模型请求**需要经过脱敏；`model` 控制是否启用**本地 PII 语义识别模型**。
- **`kinds` 支持的类别 ID**：`ssn`、`passport`、`iban`、`birthday`、`mac`、`serial`（默认开启）；`home`、`userhost`、`ip`、`bucket`（默认关闭）。
- **`actions` 与 `block_terms`**：动作支持 `mask`（替换占位符）、`warn`（保留原文并在审计事件/日志告警）、`block`（直接拦截并返回 HTTP 400，不请求上游）。若同时配置 `block_terms: true` 与显式的 `actions.TERM`，以显式的 `actions.TERM` 为准。
- **`key_path` 持久化**：未配置时每次重启 CPA 会使用临时内存密钥；配置 `key_path`（或 `CPA_REDUCTANT_KEY_PATH`）后会持久化 32 字节 HMAC 密钥，使跨重启的历史对话仍可还原此前生成的占位符。注意：首次脱敏发生后，热重载不能动态更换 `key_path`，修改需重启 CPA 生效。

---

## 本地 PII 语义识别模型（AstrLink Guard）

内建规则适合密钥、邮箱、手机号等强格式内容；如果需要进一步识别**人名、地址、机构名**等非结构化个人隐私，可以开启内嵌的轻量本地模型 `astrlink-guard`（约 39 MB int8 量化 BERT）。由于采用纯 CPU / macOS CoreML 本地推理，推理速度可能较慢，长文本场景会受 `wait_ms` 超时约束，建议按需开启。

### 1. 在线自动启用（最小配置）

```yaml
model:
  enabled: true
```

启用后插件会在后台异步拉取模型权重（优先 `huggingface.co`，失败自动回退 `hf-mirror.com`），支持 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量、断点续传与 SHA-256 校验；全程异步进行，CPA 启动后即可立即处理正常请求。

### 2. 模型存放路径（`model.dir`）

目录解析优先级如下：

1. YAML 中的 `model.dir`
2. 环境变量 `CPA_REDUCTANT_MODEL_DIR`
3. 各平台默认缓存目录：
   - **Linux**：`$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard`（未设置 `$XDG_CACHE_HOME` 时为 `~/.cache/cpa-reductant/models/astrlink-guard`）
   - **macOS**：`~/Library/Caches/cpa-reductant/models/astrlink-guard`
   - **Windows**：`%LocalAppData%\cpa-reductant\models\astrlink-guard`

> 如果 CPA 作为 `systemd` 服务或无 `HOME` 的容器用户运行，建议显式配置 `model.dir` 或 `CPA_REDUCTANT_MODEL_DIR`。

### 3. 离线手动部署

如果运行环境无法访问外网，可前往 [QuantumNous/astrlink-guard](https://huggingface.co/QuantumNous/astrlink-guard/tree/main/int8)（或镜像 [hf-mirror.com](https://hf-mirror.com/QuantumNous/astrlink-guard/tree/main/int8)）下载 `int8/` 下的 `model_int8.onnx`、`model_int8.onnx_data` 和 `tokenizer.json` 三个文件，直接平铺放入模型目录，并将 `auto_download` 设为 `false`。

也可在管理看板或 `GET /v0/management/cpa-reductant/status` 的 `model.revision` 字段查看当前插件锁定的 revision，使用 `curl` 在可联网的机器上拉取后拷贝至目标目录：

```sh
MODEL_DIR="/path/to/astrlink-guard"
REV="main" # 或替换为 /v0/management/cpa-reductant/status 返回的 model.revision
BASE="https://huggingface.co/QuantumNous/astrlink-guard/resolve/${REV}/int8" # 国内可换为 hf-mirror.com
mkdir -p "$MODEL_DIR"
for f in model_int8.onnx model_int8.onnx_data tokenizer.json; do
  curl -fL "$BASE/$f" -o "$MODEL_DIR/$f"
done
```

```yaml
model:
  enabled: true
  auto_download: false
  dir: /path/to/astrlink-guard
```

### 4. 模型参数与运行特性

- **`min_score`**：置信度阈值，范围 `[0.5, 1.0)`，默认 `0.8`。越低越敏感，越高越保守。
- **`strict`**：默认 `false`。
  - `false`（推荐）：模型未就绪或单次等待超时时，保留已扫描片段并自动降级为规则脱敏；
  - `true`：模型缺失或损坏时直接返回 HTTP `503`（不触发 CPA 上游凭据冷却）；若长会话持续因新文本超时，会临时收敛等待时间至 `2000ms` 防止请求雪崩。
- **`wait_ms`**：单次请求等待模型推理的上限毫秒数（默认：非严格模式 `2000`，严格模式 `15000`）。
- **`mirror`**：自定义下载镜像地址（留空时 `huggingface.co` 失败自动回退 `hf-mirror.com`）。
- **运行方式与缓存**：纯进程内 CPU / macOS CoreML 推理，随插件进程直接运行；支持 4 KB 稳定分块与进程内 LRU / 磁盘特征缓存（`<dir>/found-<rev>.bin` 仅保存哈希与偏移量，只存特征索引、不落盘原文）。

---

## 管理面板与运维接口

### 打开可视化面板

1. 打开 CPA 管理后台页面（`management.html`），在左侧「插件」菜单中点击 `cpa-reductant`（或直接访问 `/v0/resource/plugins/cpa-reductant/ui`）。
2. 输入 `config.yaml` 中配置的 `management.secret-key` 登录，即可进入插件面板。

### 面板常用功能

- **查看运行概览**：实时展示当前生效策略、脱敏/拦截/还原次数、处理耗时、各协议/模型流量分布，以及近期事件记录（仅展示脱敏后信息与类型，不记录明文）。
- **测试脱敏规则（在线预览）**：输入一段示例文本即可实时查看会被哪些规则命中、替换成什么占位符，试跑过程不影响统计数据，也不会保存映射记录。
- **排查具体请求（临时调试抓包）**：遇到规则误伤或还原异常时，可在面板中临时开启抓包（仅存内存，默认 5 分钟自动过期、最长 30 分钟），直接对照查看「原始请求 / 脱敏后请求 / 上游返回 / 还原后返回」，排查完毕可随时关闭并一键清空缓存。
- **管理本地模型**：查看 `astrlink-guard` 权重状态、解析路径与下载进度；如果后台拉取失败，可在面板中点击按钮重试下载。

> 所有拦截、降级与模型下载事件也会同步写入 CPA 宿主日志。

<details>
<summary>自动化运维 / HTTP 接口一览（均需携带 <code>management.secret-key</code> 鉴权）</summary>

| 接口 | 用途 | 常用参数 / 请求体 |
| --- | --- | --- |
| `GET /v0/management/cpa-reductant/ui` | 服务端渲染 HTML 监控看板 | `?refresh=5`（自动刷新秒数）、`?events=N`、`?action=...` |
| `GET /v0/management/cpa-reductant/status` | 获取 JSON 全量状态与统计快照 | `?format=html`、`?events=N`、`?action=mask\|warn\|block\|restore\|passthrough\|error` |
| `POST /v0/management/cpa-reductant/preview` | 只读测试脱敏效果 | `{"text": "..."}` |
| `GET /v0/management/cpa-reductant/debug/capture` | 读取当前内存调试抓包记录 | `?id=`、`?limit=`、`?protocol=`、`?model=` |
| `POST /v0/management/cpa-reductant/debug/capture` | 开启/关闭或重置抓包 TTL | `{"enabled": true, "ttl_seconds": 300}` |
| `DELETE /v0/management/cpa-reductant/debug/capture` | 立即清空全部抓包缓存 | — |
| `POST /v0/management/cpa-reductant/model/download` | 手动触发或重试本地模型下载 | `{"mirror": "..."}`（可选） |

</details>

---

## 支持范围与当前限制

### 已支持的协议与端点（含 JSON 与流式 SSE）

- `claude`：`/v1/messages`、`/v1/messages/count_tokens`
- `openai`：`/v1/chat/completions`、`/v1/completions`
- `openai-response`：`/v1/responses`、`/v1/responses/compact`、`/backend-api/codex/responses`（支持 WebSocket `/v1/responses` 客户端接入，上游转发回退为 HTTP）
- `gemini`：`/v1beta/models/*:generateContent`、`:streamGenerateContent`、`:countTokens`
- `interactions`：`/v1beta/interactions`（同时支持 `model` 与 `agent` 请求）
- `openai-image` / `openai-speech` / `openai-video`：文生图/改图、TTS（保留二进制音频响应）、视频生成
- `codex` / `antigravity`：SDK 内部格式

### 当前限制

- 本地 PII 模型当前仅支持 `astrlink-guard`（CPU / macOS CoreML），暂未接入依赖独立 `pf-serve` 进程的 `privacy-filter` 与 Linux Vulkan/GPU 加速。
- 暂不支持 CPA Home 模式与 `/v1/alpha/search`（CPA 原生限制）。
- 占位符还原映射在进程内共享，暂未按多租户隔离；插件未加载或路由失败时，CPA 会回退原生路由直出。

---

## 开发者

需要从源码编译、运行测试、查看项目目录结构、了解 `patches/magpie/` 补丁机制或使用 Nix 开发环境，请查看 [开发者指南（docs/DEVELOPMENT.md）](docs/DEVELOPMENT.md)。

---

## Credit

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)：提供原生插件 ABI 宿主与多协议代理底座
- [Magpie](https://github.com/yetone/magpie)：提供核心可逆脱敏引擎（`redact`）与本地模型推理封装（`guard`）
- [AstrLink Guard](https://huggingface.co/QuantumNous/astrlink-guard)：提供轻量级本地 PII 语义识别模型

## License

本项目基于 [MIT License](LICENSE) 开源。
