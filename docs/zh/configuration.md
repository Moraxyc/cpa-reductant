[English](../en/configuration.md) | **简体中文**

# 配置参考

配置写在 CPA `config.yaml` 的 `plugins.configs.cpa-reductant` 下。

## 配置示例

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true

      models: []                    # 上游模型 ID 或通配符（*、?）；留空匹配全部
      exclude_models: []            # 跳过的模型 ID 或通配符（优先级高于 models）
      protocols: []                 # 生效协议；留空匹配全部受支持协议

      secrets: true                 # 内建密钥检测
      personal: true                # 内建个人隐私检测
      kinds:                        # 按类别覆写个人隐私开关
        ssn: true                   # 默认开启：ssn, passport, iban, birthday, mac, serial
        home: false                 # 默认关闭：home, userhost, ip, bucket

      words: [example-private-term] # 字面量自定义敏感词
      words_any_case: false
      words_whole: false
      allow: [ops@example.org]      # 明文放行的精确值
      # rules:                      # 最多 32 条自定义规则；prefix 与 regex 二选一
      #   - kind: CORP_KEY
      #     prefix: crp_
      #   - kind: TICKET
      #     regex: 'JIRA-\d{4,}'

      # actions:                    # 按类别指定动作：mask | warn | block（HTTP 400）
      #   EMAIL: warn
      #   PRIVATE_KEY: block
      block_terms: false            # 等价于 actions.TERM: block
      shape: false                  # 使用保形伪造值替代 {{KIND_...}} 占位符
      # key_path: /var/lib/cpa/reductant.key # 或环境变量 CPA_REDUCTANT_KEY_PATH
      scope_isolation: false        # 按 CPA CallerScope 隔离占位符还原
      # scope_ttl_seconds: 86400

      # alerts:
      #   webhook_url: https://hooks.example.internal/cpa-alerts
      #   events: [block, warn]     # block | warn | error（默认 [block]）
      #   timeout_ms: 5000
      #   headers:
      #     Authorization: "Bearer <token>"

      model:                        # PII 检测模型（见 local-model.md）
        enabled: false
        provider: astrlink-guard    # astrlink-guard | http
        auto_download: true
        name: astrlink-guard
        # dir: /path/to/astrlink-guard
        min_score: 0.8
        strict: false
        # wait_ms: 2000
        # mirror: https://hf-mirror.com

      # policies:                   # 按模型/协议覆写策略，按声明顺序依次叠加
      #   - name: strict-external-claude
      #     models: ["claude-*"]
      #     exclude_models: ["claude-haiku-*"]
      #     protocols: [claude]
      #     block_terms: true
      #     model:
      #       enabled: true
      #       min_score: 0.75
```

## 字段说明与优先级

### 路由匹配（`models`、`exclude_models`、`protocols`）

- `models`（复数）决定拦截哪些上游 LLM 请求；`model`（单数）配置 PII 识别模型。
- `models` 与 `exclude_models` 都支持 `*`（匹配含 `/` 在内的任意字符）和 `?`（单字符）。命中 `exclude_models` 时始终跳过脱敏。
- `protocols` 支持常见别名：`anthropic` → `claude`、`chat-completions` / `openai-chat-completions` → `openai`、`responses` / `openai-responses` → `openai-response`。

### 分级策略（`policies`）

所有匹配当前请求（`models`、`exclude_models`、`protocols`）的条目会按声明顺序依次合并：

- 布尔开关（`secrets`、`personal`、`words_any_case`、`words_whole`、`shape`、`block_terms`）和 `model` 字段（`enabled`、`min_score`、`strict`、`wait_ms`）显式设置时覆写基础值。
- `kinds` 与 `actions` 按键合并。显式设为 `mask` 可以取消基础配置里的 `warn` 或 `block`。
- `words`、`rules`、`allow` 追加到基础列表之后。

### 规则、词表与动作

- `home`、`userhost`、`ip`、`bucket` 默认关闭，避免编程场景下误伤本地路径和常见网络地址。
- 同一个词同时出现在 `words` 和 `allow` 时，`words` 优先。
- `rules` 最多 32 条；每条需指定 `kind`，并在 `prefix` 与 `regex` 中选一个。
- `actions` 可选值：`mask`（替换为占位符并在响应时还原）、`warn`（保留原文，记审计事件和宿主日志）、`block`（直接返回 HTTP `400`，不发往上游）。
- 同时配置 `block_terms: true` 和 `actions.TERM` 时，以 `actions.TERM` 为准。

### 密钥、租户隔离与告警

- `key_path`（或 `CPA_REDUCTANT_KEY_PATH`）用于持久化 32 字节 HMAC 密钥，确保 CPA 重启后历史对话中的占位符仍可还原；未配置则每次启动重新生成内存密钥。首次脱敏发生后，热重载不会切换 `key_path`，修改需重启 CPA。
- 开启 `scope_isolation` 后，插件会在有界 LRU/TTL 表（`scope_ttl_seconds` 默认 `86400`）中按 CPA `CallerScope` 记录每个租户生成的占位符，响应与 SSE 流只还原同一作用域下的占位符。
- `alerts` 通过有界异步队列向 `alerts.webhook_url` 发送不含明文的 `FilterEvent` JSON；Webhook 变慢或失败时会丢弃溢出事件，不阻塞请求处理。

## 支持的协议与端点

除特别注明外，均同时支持 JSON 与流式 SSE：

- `claude`：`/v1/messages`、`/v1/messages/count_tokens`
- `openai`：`/v1/chat/completions`、`/v1/completions`
- `openai-response`：`/v1/responses`、`/v1/responses/compact`、`/backend-api/codex/responses`（客户端支持 WebSocket `/v1/responses`，向上游转发走 HTTP）
- `gemini`：`/v1beta/models/*:generateContent`、`:streamGenerateContent`、`:countTokens`
- `interactions`：`/v1beta/interactions`（`model` 与 `agent` 请求）
- `openai-image`、`openai-speech`（二进制音频响应原样透传）、`openai-video`
- `codex`、`antigravity`

## 当前限制

- 进程内推理仅支持 `astrlink-guard`（CPU / macOS CoreML）。如需 GPU 或独立推理服务，请将 `model.provider` 设为 `http` 对接外部通用或 Presidio 兼容 NER 服务。
- 不支持 CPA Home 模式与 `/v1/alpha/search`（CPA 宿主限制）。
- `scope_isolation` 为 `false` 时，还原映射在进程内共享；插件卸载或路由失败时，CPA 回退到原生上游转发。
