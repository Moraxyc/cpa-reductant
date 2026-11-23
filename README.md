**English** | [简体中文](docs/zh/README.md)

# CPA Reductant

A reversible privacy redaction plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA). Drop it into CPA's plugins directory to mask secrets, personal data, and custom sensitive terms before prompts leave for upstream LLMs, and automatically restore them in both JSON and streaming SSE responses.

## How It Works

```text
Client ──▶ CLIProxyAPI + cpa-reductant (Mask / Warn / Block) ──▶ Upstream LLM
Client ◀── CLIProxyAPI + cpa-reductant (Restore, JSON & SSE) ◀── Upstream LLM
```

- **Reversible redaction**: replaces matched text with deterministic HMAC placeholders (or optional shape-preserving fakes) outbound, and restores the original values seamlessly on the way back.
- **Layered detection**: combines built-in secret and structured PII rules (with `home`, `userhost`, `ip`, and `bucket` disabled by default to avoid coding-path false positives), custom wordlists/prefix/regex rules, and an optional embedded local PII model (`astrlink-guard`).
- **Zero-plaintext observability**: ships an authenticated management dashboard with live metrics, rule preview, and temporary in-memory debug capture without ever logging raw secrets.
- **Broad protocol coverage**: works across `claude`, `openai`, `openai-response` (including WebSocket client input), `gemini`, `interactions`, `openai-image` / `openai-speech` / `openai-video`, `codex`, and `antigravity`.

## Installation & Quick Start

1. **Get the plugin**:
   - **Prebuilt binaries**: download `cpa-reductant_<version>_<os>_<arch>.zip` (`linux`/`darwin`/`windows` × `amd64`/`arm64`, verifiable via [checksums.txt](https://github.com/Moraxyc/cpa-reductant/releases/latest/download/checksums.txt)) from [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) and extract the shared library (`.so` / `.dylib` / `.dll`) into CPA's `plugins.dir`.
   - **Nix / NixOS**: use `packages.<system>.cpa-reductant` or `overlays.default` (`pkgs.cpa-reductant`); output is located at `$out/lib/cliproxyapi/plugins/<os>/<arch>/cpa-reductant.<ext>`.

2. **Enable in `config.yaml`** and start or reload CPA:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
```

By default, all configured upstream models and supported protocols are protected with built-in secret and personal-data rules enabled.

## Documentation

| Topic | Guide | Covers |
| --- | --- | --- |
| Configuration | [Configuration Reference](docs/en/configuration.md) | Scope (`models` vs `model`), built-in `kinds`, `words`/`rules`/`allow`, `actions`, `shape`, `key_path`, supported endpoints & limitations |
| Semantic PII | [Local PII Model (AstrLink Guard)](docs/en/local-model.md) | Online auto-download, `model.dir` resolution, air-gapped setup, `min_score`/`strict`/`wait_ms`, caching |
| Operations | [Management Dashboard & API](docs/en/management.md) | Web UI portal, masking preview, debug capture, model downloads, HTTP endpoints |
| Contributing | [Developer Guide](docs/en/DEVELOPMENT.md) | Architecture, Magpie patches, Nix & native builds, testing, CI/release, upstream sync |

## Credit

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI): native plugin ABI host and multi-protocol proxy foundation
- [Magpie](https://github.com/yetone/magpie): reversible redaction engine (`redact`) and local model inference wrapper (`guard`)
- [AstrLink Guard](https://huggingface.co/QuantumNous/astrlink-guard): lightweight local PII semantic recognition model

## License

Open-sourced under the [MIT License](LICENSE).
