**English** | [简体中文](../zh/local-model.md)

# PII Detection Model

Built-in rules cover structured values like API keys, emails, and phone numbers. To also catch unstructured PII (names, addresses, organizations), enable either the embedded local model `astrlink-guard` (~39 MB int8 BERT, CPU / macOS CoreML) or a remote HTTP NER service.

Because local CPU inference is slower on long inputs and bounded by `wait_ms`, leave it disabled unless you need unstructured PII detection.

## 1. Local Model: Auto-Download

```yaml
model:
  enabled: true
```

Weights download asynchronously in the background (`huggingface.co` first, falling back to `hf-mirror.com`), with `HTTP_PROXY`/`HTTPS_PROXY` support, resume, and SHA-256 verification. CPA serves requests immediately while weights download.

## 2. Model Directory (`model.dir`)

Resolution order:

1. `model.dir` in `config.yaml`
2. `CPA_REDUCTANT_MODEL_DIR`
3. Default cache directory:
   - Linux: `$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard` (or `~/.cache/...`)
   - macOS: `~/Library/Caches/cpa-reductant/models/astrlink-guard`
   - Windows: `%LocalAppData%\cpa-reductant\models\astrlink-guard`

When running under `systemd` or a container without `HOME`, set `model.dir` or `CPA_REDUCTANT_MODEL_DIR` explicitly.

## 3. Offline Setup

Download `model_int8.onnx`, `model_int8.onnx_data`, and `tokenizer.json` from `int8/` at [QuantumNous/astrlink-guard](https://huggingface.co/QuantumNous/astrlink-guard/tree/main/int8) (or [hf-mirror.com](https://hf-mirror.com/QuantumNous/astrlink-guard/tree/main/int8)), put them in the model directory, and set `auto_download: false`.

You can read the pinned revision from `model.revision` in `GET /v0/management/cpa-reductant/status`:

```sh
MODEL_DIR="/path/to/astrlink-guard"
REV="main" # Or model.revision from /v0/management/cpa-reductant/status
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

## 4. Runtime Options

- `min_score`: threshold in `[0.5, 1.0)`, default `0.8`.
- `strict`: default `false`.
  - `false`: if the model is not ready or times out, keeps already-scanned chunks and falls back to rule-based redaction.
  - `true`: returns HTTP `503` if weights are missing or invalid (without triggering CPA upstream credential cooldown). After consecutive timeouts on uncached text, the per-request wait cap tightens to `2000ms` until inference recovers.
- `wait_ms`: per-request wait cap in milliseconds (defaults: `2000` non-strict, `15000` strict).
- `mirror`: custom download mirror URL (replaces the default `huggingface.co` → `hf-mirror.com` fallback).
- Caching: inputs are split into 4 KB chunks with an in-memory LRU cache and an on-disk cache (`<dir>/found-<rev>.bin`) storing hashes and byte offsets only.

## 5. Remote HTTP NER (`provider: http`)

To run NER on a GPU host or an external service (such as Microsoft Presidio or a custom GLiNER endpoint), set `model.provider: http`. Remote requests share the same 4 KB chunking, in-memory LRU cache, `wait_ms` budget, and `strict` behavior:

```yaml
model:
  enabled: true
  provider: http
  format: presidio              # generic (default) | presidio
  endpoint: http://127.0.0.1:5001/analyze
  language: en                  # Presidio language parameter (default: en)
  min_score: 0.8
  wait_ms: 2000
  headers:
    Authorization: "Bearer <optional-token>"
```

- `generic`: `POST {"text": "..."}` expecting `{"spans": [{"start": 0, "end": 4, "label": "private_person", "score": 0.95}]}` with UTF-8 byte offsets (top-level arrays and `results` are also accepted).
- `presidio`: `POST {"text": "...", "language": "...", "score_threshold": 0.8}` expecting `[{"start": 0, "end": 4, "entity_type": "PERSON", "score": 0.95}]`; character offsets are converted to UTF-8 byte offsets.
