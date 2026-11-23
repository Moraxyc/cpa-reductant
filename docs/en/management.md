**English** | [简体中文](../zh/management.md)

# Management Dashboard & Operations API

`cpa-reductant` provides a built-in web dashboard and authenticated HTTP endpoints for monitoring redaction activity, testing rules, debugging request diffs, and managing local model weights. Return to the [README](../../README.md) for overview and installation.

## Opening the Web Dashboard

1. Open the CPA management console (`management.html`) and click `cpa-reductant` under the **Plugins** menu on the left (or visit `/v0/resource/plugins/cpa-reductant/ui` directly).
2. Sign in with `management.secret-key` from `config.yaml` (stored in the current browser tab only).

## Dashboard Capabilities

- **Live overview**: view active policy switches, mask/warn/block/restore counts, average processing latency, per-protocol and per-model traffic breakdown, and recent audit events (recording redacted tokens and types only, never plaintext).
- **Masking preview**: test plain text or full API JSON request bodies (`mode: "json"`) against the active policy, a specific `model`/`protocol` override, or temporary what-if `override` fields (`words`, `allow`, `shape`, etc.) without affecting counters or persisting mappings.
- **Debug capture**: temporarily enable in-memory request/response diffing (defaults to 5 minutes TTL, up to 30 minutes max) to compare **Raw request / Masked request → upstream / Upstream response / Restored response → client** with inline token highlighting, one-click JSON export, and copyable `allow` YAML snippets, then stop and clear the buffer anytime.
- **Local model management**: monitor `astrlink-guard` weight status, resolved directory, and download progress, or trigger/retry weight downloads directly from the UI.

> All block, fallback/degraded, and model download events are also logged to the CPA host logs.

## HTTP Operations API

All endpoints below require `management.secret-key` authentication (for example via `Authorization: Bearer <secret-key>` or `X-Management-Key: <secret-key>`).

| Endpoint | Purpose | Common Query Params / Request Body |
| --- | --- | --- |
| `GET /v0/management/cpa-reductant/ui` | Server-rendered HTML dashboard | `?refresh=5` (auto-refresh interval in seconds), `?events=N`, `?action=...` |
| `GET /v0/management/cpa-reductant/status` | Full JSON status and metrics snapshot | `?format=html`, `?events=N`, `?action=mask\|warn\|block\|restore\|passthrough\|error` |
| `GET /v0/management/cpa-reductant/metrics` | Prometheus text exposition metrics (`0.0.4`) | None |
| `POST /v0/management/cpa-reductant/preview` | Read-only masking preview (text or JSON, with optional what-if overrides) | `{"text": "...", "mode": "text\|json", "protocol": "...", "model": "...", "override": {...}}` |
| `GET /v0/management/cpa-reductant/debug/capture` | Read in-memory debug captures | `?id=`, `?limit=`, `?protocol=`, `?model=` |
| `POST /v0/management/cpa-reductant/debug/capture` | Enable/disable capture or update TTL | `{"enabled": true, "ttl_seconds": 300}` |
| `DELETE /v0/management/cpa-reductant/debug/capture` | Clear all cached captures immediately | None |
| `POST /v0/management/cpa-reductant/model/download` | Trigger or retry local model download | `{"mirror": "..."}` (optional) |
