**English** | [简体中文](../zh/management.md)

# Management UI & Operations API

## Web Dashboard

Open CPA's console (`management.html`) and select `cpa-reductant` under **Plugins**, or open `/v0/resource/plugins/cpa-reductant/ui` directly. Authenticate with `management.secret-key` from `config.yaml` (kept in browser `sessionStorage` for the current tab).

What the UI exposes:

- **Status & events**: active policy switches, mask/warn/block/restore counters, average and P95 latency, per-protocol and per-model breakdowns, and recent audit events (placeholders and kinds only, never plaintext).
- **Preview**: test plain text or JSON request payloads (`mode: "json"`) against the active config, a target `model`/`protocol` policy, or temporary `override` fields (`words`, `allow`, `shape`, etc.). Previews do not mutate counters or placeholder mappings.
- **Debug capture**: temporarily capture request/response pairs in memory (default TTL 5 min, max 30 min) to inspect raw request, masked request, upstream response, and restored response with inline token highlighting, JSON export, and copyable `allow` YAML snippets.
- **Local model**: view `astrlink-guard` weight status, resolved path, and download progress, or trigger/retry a download.

Block, fallback, and model download events are also written to CPA host logs.

## HTTP API

All endpoints require `management.secret-key` via `Authorization: Bearer <secret-key>` or `X-Management-Key: <secret-key>`.

| Endpoint | Purpose | Query Params / JSON Body |
| --- | --- | --- |
| `GET /v0/management/cpa-reductant/ui` | Server-rendered HTML dashboard | `?refresh=5`, `?events=N`, `?action=...` |
| `GET /v0/management/cpa-reductant/status` | JSON status and metrics snapshot | `?format=html`, `?events=N`, `?action=mask\|warn\|block\|restore\|passthrough\|error` |
| `GET /v0/management/cpa-reductant/metrics` | Prometheus metrics (`0.0.4` text format) | — |
| `POST /v0/management/cpa-reductant/preview` | Side-effect-free masking preview | `{"text": "...", "mode": "text\|json", "protocol": "...", "model": "...", "override": {...}}` |
| `GET /v0/management/cpa-reductant/debug/capture` | List or fetch in-memory captures | `?id=`, `?limit=`, `?protocol=`, `?model=` |
| `POST /v0/management/cpa-reductant/debug/capture` | Enable/disable capture or update TTL | `{"enabled": true, "ttl_seconds": 300}` |
| `DELETE /v0/management/cpa-reductant/debug/capture` | Clear captured entries | — |
| `POST /v0/management/cpa-reductant/model/download` | Start or retry local model download | `{"mirror": "..."}` (optional) |
