**English** | [简体中文](../zh/local-model.md)

# Local PII Semantic Model (AstrLink Guard)

Built-in rules handle structured patterns such as API keys, emails, and phone numbers. When you also need to detect unstructured personal data (such as **names, street addresses, and organization names**), enable the embedded lightweight local model `astrlink-guard` (~39 MB int8-quantized BERT).

Because inference runs locally on CPU or macOS CoreML, processing long prompts can be slower and is bounded by `wait_ms`. Enable this feature only when semantic PII detection is needed. For general plugin configuration, see [Configuration Reference](configuration.md) or return to the [README](../../README.md).

## 1. Online Auto-Setup (Minimal Config)

```yaml
model:
  enabled: true
```

Once enabled, the plugin asynchronously downloads model weights in the background (`huggingface.co` first, automatically falling back to `hf-mirror.com`), supporting:

- `HTTP_PROXY` / `HTTPS_PROXY` environment variables
- Resumable downloads
- SHA-256 checksum verification

Downloading happens completely asynchronously so CPA starts serving normal requests immediately.

## 2. Model Directory (`model.dir`)

Directory resolution follows this precedence order:

1. `model.dir` in `config.yaml`
2. Environment variable `CPA_REDUCTANT_MODEL_DIR`
3. Platform default cache directory:
   - **Linux**: `$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard` (defaults to `~/.cache/cpa-reductant/models/astrlink-guard` when `$XDG_CACHE_HOME` is unset)
   - **macOS**: `~/Library/Caches/cpa-reductant/models/astrlink-guard`
   - **Windows**: `%LocalAppData%\cpa-reductant\models\astrlink-guard`

> When running CPA as a `systemd` service or inside a container without `HOME`, explicitly configure `model.dir` or `CPA_REDUCTANT_MODEL_DIR`.

## 3. Offline Manual Deployment

In air-gapped environments, download `model_int8.onnx`, `model_int8.onnx_data`, and `tokenizer.json` from `int8/` at [QuantumNous/astrlink-guard](https://huggingface.co/QuantumNous/astrlink-guard/tree/main/int8) (or the [hf-mirror.com](https://hf-mirror.com/QuantumNous/astrlink-guard/tree/main/int8) mirror), place them directly into the target model directory, and set `auto_download: false`.

You can check the pinned `model.revision` in the management dashboard or via `GET /v0/management/cpa-reductant/status`, fetch the files on an online host, and copy them over:

```sh
MODEL_DIR="/path/to/astrlink-guard"
REV="main" # Or replace with model.revision from /v0/management/cpa-reductant/status
BASE="https://huggingface.co/QuantumNous/astrlink-guard/resolve/${REV}/int8" # Or hf-mirror.com
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

## 4. Parameters & Runtime Behavior

- **`min_score`**: confidence threshold in `[0.5, 1.0)`, default `0.8`. Lower values are more sensitive; higher values are more conservative.
- **`strict`**: default `false`.
  - `false` (recommended): when the model is not ready or times out, keeps any chunks already scanned and falls back to rule-based redaction.
  - `true`: returns HTTP `503` if weights are missing or corrupted (without triggering CPA upstream credential cooldown); if consecutive timeouts occur on new text in long sessions, the wait cap temporarily tightens to `2000ms` to prevent request pile-ups.
- **`wait_ms`**: per-request inference wait cap in milliseconds (defaults: `2000` when non-strict, `15000` when strict).
- **`mirror`**: custom download mirror URL (when omitted, failed `huggingface.co` downloads fall back to `hf-mirror.com`).
- **Execution & caching**: runs in-process on CPU / macOS CoreML with 4 KB stable chunking, an in-memory LRU cache, and a disk feature cache (`<dir>/found-<rev>.bin` stores hashes and offsets only; no plaintext is ever written to disk).

## 5. Remote HTTP NER Backend (`provider: http`)

To offload semantic PII detection to a dedicated GPU or external NER service (such as Microsoft Presidio or a custom GLiNER service), set `model.provider: http`. It reuses the same 4 KB chunking, in-memory LRU caching, in-flight deduplication, `wait_ms` budget, and `strict` fallback logic:

```yaml
model:
  enabled: true
  provider: http
  format: presidio              # generic (default) | presidio
  endpoint: http://127.0.0.1:5001/analyze
  language: en                  # Sent to Presidio analyzer (default: en)
  min_score: 0.8
  wait_ms: 2000
  headers:
    Authorization: "Bearer <optional-token>"
```

- **`generic` format**: `POST` `{"text": "..."}` expecting `{"spans": [{"start": 0, "end": 4, "label": "private_person", "score": 0.95}]}` (UTF-8 byte offsets; top-level JSON arrays and `results` are also accepted).
- **`presidio` format**: `POST` `{"text": "...", "language": "...", "score_threshold": 0.8}` expecting Presidio's JSON array `[{"start": 0, "end": 4, "entity_type": "PERSON", "score": 0.95}]` (character/rune offsets are automatically converted to UTF-8 byte offsets).
