**English** | [简体中文](../zh/configuration.md)

# Configuration Reference

Options live under `plugins.configs.cpa-reductant` in CPA's `config.yaml`.

## Example

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true

      models: []                    # Model IDs or globs (*, ?); empty matches all models
      exclude_models: []            # Model IDs or globs to skip (takes precedence over models)
      protocols: []                 # Protocol filter; empty matches all supported protocols

      secrets: true                 # Built-in secret detectors
      personal: true                # Built-in personal-data detectors
      kinds:                        # Override individual personal-data kinds
        ssn: true                   # On by default: ssn, passport, iban, birthday, mac, serial
        home: false                 # Off by default: home, userhost, ip, bucket

      words: [example-private-term] # Literal terms to mask
      words_any_case: false
      words_whole: false
      allow: [ops@example.org]      # Exact values kept in plaintext
      # rules:                      # Max 32 custom rules; set either prefix or regex
      #   - kind: CORP_KEY
      #     prefix: crp_
      #   - kind: TICKET
      #     regex: 'JIRA-\d{4,}'

      # actions:                    # Per-kind action: mask | warn | block (HTTP 400)
      #   EMAIL: warn
      #   PRIVATE_KEY: block
      block_terms: false            # Equivalent to actions.TERM: block
      shape: false                  # Shape-preserving fake values instead of {{KIND_...}}
      # key_path: /var/lib/cpa/reductant.key # Or env CPA_REDUCTANT_KEY_PATH
      scope_isolation: false        # Isolate restoration by CPA CallerScope
      # scope_ttl_seconds: 86400

      # alerts:
      #   webhook_url: https://hooks.example.internal/cpa-alerts
      #   events: [block, warn]     # block | warn | error (default: [block])
      #   timeout_ms: 5000
      #   headers:
      #     Authorization: "Bearer <token>"

      model:                        # PII model (see local-model.md)
        enabled: false
        provider: astrlink-guard    # astrlink-guard | http
        auto_download: true
        name: astrlink-guard
        # dir: /path/to/astrlink-guard
        min_score: 0.8
        strict: false
        # wait_ms: 2000
        # mirror: https://hf-mirror.com

      # policies:                   # Route-specific overrides, applied in declaration order
      #   - name: strict-external-claude
      #     models: ["claude-*"]
      #     exclude_models: ["claude-haiku-*"]
      #     protocols: [claude]
      #     block_terms: true
      #     model:
      #       enabled: true
      #       min_score: 0.75
```

## Notes & Precedence

### Routing (`models`, `exclude_models`, `protocols`)

- `models` (plural) selects upstream LLM models to intercept; `model` (singular) configures the PII detection model.
- Both `models` and `exclude_models` support `*` (matches any characters, including `/`) and `?` (single character). `exclude_models` wins over `models`.
- `protocols` accepts aliases: `anthropic` → `claude`, `chat-completions` / `openai-chat-completions` → `openai`, `responses` / `openai-responses` → `openai-response`.

### Policy Overrides (`policies`)

Every entry whose `models`, `exclude_models`, and `protocols` match the request is applied in order:

- Scalar booleans (`secrets`, `personal`, `words_any_case`, `words_whole`, `shape`, `block_terms`) and `model` fields (`enabled`, `min_score`, `strict`, `wait_ms`) replace the base value when set.
- `kinds` and `actions` merge by key. Setting an action to `mask` clears a base `warn` or `block`.
- `words`, `rules`, and `allow` append to the base lists.

### Built-in Rules, Wordlists, and Actions

- `home`, `userhost`, `ip`, and `bucket` default to `false` so local file paths and common addresses are not masked in coding sessions.
- If a value appears in both `words` and `allow`, `words` wins.
- `rules` is capped at 32 entries; each entry sets `kind` plus either `prefix` or `regex` (not both).
- `actions` values: `mask` (replace and restore), `warn` (keep plaintext, emit audit event and host log), `block` (reject with HTTP `400` before forwarding upstream).
- An explicit `actions.TERM` overrides `block_terms: true`.

### Keys, Tenant Isolation, and Alerts

- `key_path` (or `CPA_REDUCTANT_KEY_PATH`) persists the 32-byte HMAC key so placeholders from prior turns still restore after a restart. Without it, a fresh key is generated on each start. Once the first redaction runs, changing `key_path` requires a CPA restart; hot-reload will not swap it.
- `scope_isolation` tracks emitted placeholders per CPA `CallerScope` in an LRU/TTL table (`scope_ttl_seconds`, default `86400`). A response or SSE stream only restores placeholders created under the same scope.
- `alerts` posts sanitized `FilterEvent` JSON (no plaintext) to `alerts.webhook_url` from a bounded async queue. Slow or failing webhooks drop excess events rather than blocking requests.

## Supported Protocols

Both JSON and streaming SSE are supported unless noted:

- `claude`: `/v1/messages`, `/v1/messages/count_tokens`
- `openai`: `/v1/chat/completions`, `/v1/completions`
- `openai-response`: `/v1/responses`, `/v1/responses/compact`, `/backend-api/codex/responses` (WebSocket client `/v1/responses` is accepted; upstream forwarding uses HTTP)
- `gemini`: `/v1beta/models/*:generateContent`, `:streamGenerateContent`, `:countTokens`
- `interactions`: `/v1beta/interactions` (`model` and `agent` requests)
- `openai-image`, `openai-speech` (binary audio responses pass through untouched), `openai-video`
- `codex`, `antigravity`

## Limitations

- In-process inference supports `astrlink-guard` on CPU and macOS CoreML. For GPU or remote inference, use `model.provider: http` with an external generic or Presidio-compatible NER server.
- CPA Home mode and `/v1/alpha/search` are not supported (CPA host limitation).
- With `scope_isolation: false`, restoration state is shared across the process. If the plugin unloads or routing fails, CPA falls back to its normal upstream routing.
