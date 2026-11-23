[English](../en/configuration.md) | **简体中文**

# 配置说明

本文档详细介绍 CLIProxyAPI `config.yaml` 中 `plugins.configs.cpa-reductant` 支持的全部配置项与协议范围。如需快速安装与启用，请查看 [README](README.md)。

## 完整配置示例

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

      # 5. 本地 PII 语义识别模型（默认关闭，见 local-model.md）
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

## 配置要点说明

### 作用范围：`models`（复数）vs `model`（单数）

- **`models`**：决定哪些**上游大模型请求**需要经过脱敏。省略或设为 `[]` 表示保护所有已配置模型。
- **`model`**：控制是否启用内嵌的**本地 PII 语义识别模型**（`astrlink-guard`），详情见 [本地 PII 模型指南](local-model.md)。
- **`protocols`**：限定生效的 CPA 协议格式，省略或 `[]` 表示覆盖全部已支持格式。兼容别名包括：`anthropic` → `claude`、`chat-completions` / `openai-chat-completions` → `openai`、`responses` / `openai-responses` → `openai-response`。

### 内建规则类别（`secrets`、`personal`、`kinds`）

- **`secrets`**（默认 `true`）：脱敏 API Key、Bearer Token、私钥、密码等凭据信息。
- **`personal`**（默认 `true`）：脱敏强格式个人隐私，可通过 `kinds` 按具体类别覆写开关：
  - **默认开启**：`ssn`、`passport`、`iban`、`birthday`、`mac`、`serial`。
  - **默认关闭**：`home`、`userhost`、`ip`、`bucket`（避免误伤编程场景中的本地路径与常见网络地址）。

### 自定义词表与正则规则（`words`、`allow`、`rules`）

- **`words`**：按字面量匹配的自定义敏感词；配合 `words_any_case: true` 可忽略大小写，配合 `words_whole: true` 仅在完整词边界匹配。
- **`allow`**：允许明文放行的精确值；若同一值同时出现在 `words` 中，仍优先受 `words` 保护。
- **`rules`**：自定义前缀或正则规则（最多 32 条），每条规则需指定 `kind`，并在 `prefix` 与 `regex` 中二选一。

### 命中动作与占位符行为（`actions`、`block_terms`、`shape`、`key_path`）

- **`actions`**：按占位符类型覆写处理动作：
  - `mask`（默认）：替换为占位符，并在响应返回时还原原文。
  - `warn`：保留原文放行，同时写入审计事件与 CPA 宿主日志告警。
  - `block`：直接拦截并返回 HTTP `400`，不向游发起请求。
- **`block_terms`**：设为 `true` 时拒绝命中 `words` 自定义词的请求（等价于 `actions.TERM: block`）。若同时配置了显式的 `actions.TERM`，以显式的 `actions.TERM` 为准。
- **`shape`**：设为 `true` 时生成保形伪造值，替代默认的 `{{KIND_...}}` 占位符。
- **`key_path`**（或环境变量 `CPA_REDUCTANT_KEY_PATH`）：持久化 32 字节 HMAC 密钥，使跨重启的历史对话仍可还原此前生成的占位符。未配置时每次重启 CPA 会生成临时内存密钥。注意：首次脱敏发生后，热重载不能动态更换 `key_path`，修改需重启 CPA 生效。

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
