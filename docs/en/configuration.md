**English** | [简体中文](../zh/configuration.md)

# Configuration Reference

This document details all configuration options under `plugins.configs.cpa-reductant` in CLIProxyAPI's `config.yaml`. For quick installation, see the [README](../../README.md).

## Full YAML Example

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true

      # 1. Scope control
      models: []                    # Target upstream LLM model IDs; omit or [] to protect all configured models (distinct from `model` below)
      protocols: []                 # Restrict active CPA protocol formats; omit or [] for all supported protocols

      # 2. Built-in rule toggles
      secrets: true                 # Default true: redact secrets, API tokens, private keys, passwords, etc.
      personal: true                # Default true: redact built-in personal data (emails, phones, IDs, bank cards, etc.)
      kinds:                        # Optional: override defaults for specific personal data categories
        ssn: true                   # Enabled by default: ssn, passport, iban, birthday, mac, serial
        home: false                 # Disabled by default: home, userhost, ip, bucket
        ip: false

      # 3. Custom word list and regex rules
      words: [example-private-term] # Custom sensitive terms to protect (literal match)
      words_any_case: false         # Case-insensitive matching for `words` when true
      words_whole: false            # Match `words` only on whole-word boundaries when true
      allow: [ops@example.org]      # Exact values allowed in plaintext (if also listed in `words`, `words` takes precedence)
      # rules:                      # Custom prefix or regex rules (max 32; specify either `prefix` or `regex`)
      #   - kind: CORP_KEY
      #     prefix: crp_
      #   - kind: TICKET
      #     regex: 'JIRA-\d{4,}'

      # 4. Match actions and placeholder behavior
      # actions:                    # Override action per placeholder kind: mask | warn | block (returns HTTP 400)
      #   EMAIL: warn
      #   PRIVATE_KEY: block
      block_terms: false            # Reject requests matching `words` when true (equivalent to actions.TERM=block)
      shape: false                  # Use shape-preserving synthetic values instead of {{...}} placeholders when true
      # key_path: /var/lib/cpa/reductant.key # Optional (or env CPA_REDUCTANT_KEY_PATH): persist placeholder HMAC key

      # 5. Local PII semantic model (disabled by default; see local-model.md)
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

## Key Configuration Details

### Scope: `models` (plural) vs `model` (singular)

- **`models`** selects which **upstream LLM requests** pass through redaction. Leaving it empty or omitted protects all configured models.
- **`model`** configures the embedded **local PII semantic recognition model** (`astrlink-guard`). See the [Local PII Model Guide](local-model.md) for details.
- **`protocols`** restricts which CPA protocol formats are intercepted. Leaving it empty or omitted covers all supported protocols. Accepted aliases include `anthropic` → `claude`, `chat-completions` / `openai-chat-completions` → `openai`, and `responses` / `openai-responses` → `openai-response`.

### Built-in Categories (`secrets`, `personal`, `kinds`)

- **`secrets`** (default `true`): masks API keys, bearer tokens, private keys, database passwords, and similar credentials.
- **`personal`** (default `true`): masks structured personal data. Individual categories can be overridden via `kinds`:
  - **Enabled by default**: `ssn`, `passport`, `iban`, `birthday`, `mac`, `serial`.
  - **Disabled by default**: `home`, `userhost`, `ip`, `bucket` (kept off to avoid false positives on local filesystem paths and common network addresses in coding workflows).

### Custom Terms & Rules (`words`, `allow`, `rules`)

- **`words`**: literal terms to mask. Use `words_any_case: true` for case-insensitive matching and `words_whole: true` to require whole-word boundaries.
- **`allow`**: exact plaintext strings excluded from masking. If a value appears in both `words` and `allow`, `words` takes precedence.
- **`rules`**: up to 32 custom rules, each defining a `kind` name together with either a `prefix` or a `regex`.

### Match Actions & Placeholder Behavior (`actions`, `block_terms`, `shape`, `key_path`)

- **`actions`**: overrides how a given placeholder `kind` is handled:
  - `mask` (default): replace with a placeholder and restore it on the response path.
  - `warn`: keep the original plaintext and record a warning in audit events and CPA host logs.
  - `block`: reject the request immediately with HTTP `400` without forwarding upstream.
- **`block_terms`**: setting `true` blocks requests containing any `words` match (equivalent to `actions.TERM: block`). If both `block_terms: true` and an explicit `actions.TERM` are set, the explicit `actions.TERM` takes precedence.
- **`shape`**: when `true`, generates shape-preserving synthetic replacements instead of `{{KIND_...}}` tokens.
- **`key_path`** (or env `CPA_REDUCTANT_KEY_PATH`): persists the 32-byte HMAC key used to derive deterministic placeholders so multi-turn conversations can still restore prior placeholders across CPA restarts. If unset, a fresh ephemeral in-memory key is used on each restart. Note: once the first redaction executes, hot-reload cannot swap `key_path` dynamically; restart CPA for changes to take effect.

## Supported Protocols & Limitations

### Supported Protocols and Endpoints (JSON & Streaming SSE)

- `claude`: `/v1/messages`, `/v1/messages/count_tokens`
- `openai`: `/v1/chat/completions`, `/v1/completions`
- `openai-response`: `/v1/responses`, `/v1/responses/compact`, `/backend-api/codex/responses` (supports WebSocket `/v1/responses` from clients; upstream forwarding falls back to HTTP)
- `gemini`: `/v1beta/models/*:generateContent`, `:streamGenerateContent`, `:countTokens`
- `interactions`: `/v1beta/interactions` (supports both `model` and `agent` requests)
- `openai-image` / `openai-speech` / `openai-video`: image generation/edits, TTS (preserves binary audio responses), video generation
- `codex` / `antigravity`: internal SDK formats

### Current Limitations

- Local PII inference currently supports `astrlink-guard` (CPU / macOS CoreML) only; `privacy-filter` (requiring a separate `pf-serve` process) and Linux Vulkan/GPU acceleration are not integrated yet.
- CPA Home mode and `/v1/alpha/search` are not supported (native CPA limitation).
- Restoration mappings are shared in-process without multi-tenant isolation; if the plugin is unloaded or routing fails, CPA falls back to native passthrough routing.
