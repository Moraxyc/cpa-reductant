**English** | [简体中文](docs/zh/README.md)

# CPA Reductant

Reversible redaction plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA). It masks secrets, personal data, and custom terms in outbound LLM requests, then restores them in JSON and streaming SSE responses.

```text
Client ──▶ CLIProxyAPI + cpa-reductant (Mask / Warn / Block) ──▶ Upstream LLM
Client ◀── CLIProxyAPI + cpa-reductant (Restore, JSON & SSE) ◀── Upstream LLM
```

## Features

- Replaces matched values with deterministic HMAC placeholders (or optional shape-preserving fakes) on requests, and restores them on responses.
- Combines built-in secret and structured PII rules (`home`, `userhost`, `ip`, and `bucket` are off by default to avoid matching local paths and addresses), custom wordlists/prefix/regex rules, and an optional PII model (`astrlink-guard` locally or an HTTP/Presidio NER service).
- Supports per-model/per-protocol policy overrides, optional per-`CallerScope` restoration isolation, async webhook alerts, Prometheus `/metrics`, and a management UI with preview and temporary in-memory debug capture.
- Works across `claude`, `openai`, `openai-response` (including WebSocket client input), `gemini`, `interactions`, `openai-image`, `openai-speech`, `openai-video`, `codex`, and `antigravity`.

## Installation

### Prebuilt binaries

Download `cpa-reductant_<version>_<os>_<arch>.zip` (`linux`/`darwin`/`windows` × `amd64`/`arm64`) from [Releases](https://github.com/Moraxyc/cpa-reductant/releases/latest) (SHA-256 in [checksums.txt](https://github.com/Moraxyc/cpa-reductant/releases/latest/download/checksums.txt)) and place the shared library (`.so` / `.dylib` / `.dll`) into CPA's `plugins.dir`.

### Nix

Use `packages.<system>.cpa-reductant` or `overlays.default` (`pkgs.cpa-reductant`). The shared library is installed at `$out/lib/cliproxyapi/plugins/<os>/<arch>/cpa-reductant.<ext>`.

### Enable in `config.yaml`

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
```

Start or reload CPA. By default, all configured models and supported protocols run with built-in secret and personal-data rules enabled.

## Documentation

- [Configuration Reference](docs/en/configuration.md) — routing globs, `policies`, built-in rules, `scope_isolation`, `alerts`, and limitations
- [PII Model Guide](docs/en/local-model.md) — local `astrlink-guard` setup, offline weights, and remote HTTP/Presidio NER
- [Management UI & API](docs/en/management.md) — dashboard, preview, debug capture, Prometheus `/metrics`, and HTTP endpoints
- [Developer Guide](docs/en/DEVELOPMENT.md) — architecture, Magpie patches, builds, tests, and upstream sync

## Credits

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — plugin ABI host and multi-protocol proxy
- [Magpie](https://github.com/yetone/magpie) — reversible redaction (`redact`) and model wrapper (`guard`)
- [AstrLink Guard](https://huggingface.co/QuantumNous/astrlink-guard) — local int8 PII NER model

## License

[MIT](LICENSE)
