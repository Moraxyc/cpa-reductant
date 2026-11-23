[English](../en/management.md) | **简体中文**

# 管理面板与接口

## Web 面板

在 CPA 管理后台（`management.html`）左侧「插件」菜单点击 `cpa-reductant`，或直接访问 `/v0/resource/plugins/cpa-reductant/ui`。使用 `config.yaml` 中的 `management.secret-key` 登录（凭证仅保存在当前标签页的 `sessionStorage`）。

面板功能：

- **状态与事件**：查看生效策略、脱敏/告警/拦截/还原计数、平均与 P95 耗时、按协议/模型分布及近期审计事件（仅含占位符与类别，不含原文）。
- **预览测试**：对纯文本或 JSON 请求体（`mode: "json"`）测试脱敏结果，可指定目标 `model`/`protocol` 或附加临时 `override`（如 `words`、`allow`、`shape`）。预览不修改统计计数，也不写入还原映射。
- **调试抓包**：临时在内存中记录请求/响应对照（默认 5 分钟过期，最长 30 分钟），对比原始请求、脱敏请求、上游响应与还原响应，支持行内高亮、导出抓包 JSON 及复制 `allow` YAML 片段，随时可停止并清空。
- **本地模型**：查看 `astrlink-guard` 权重状态、解析目录与下载进度，或在下载失败后重新触发。

拦截、降级与模型下载事件同时写入 CPA 宿主日志。

## HTTP 接口

以下接口均需携带 `management.secret-key`（`Authorization: Bearer <secret-key>` 或 `X-Management-Key: <secret-key>`）。

| 接口 | 用途 | 常用参数 / 请求体 |
| --- | --- | --- |
| `GET /v0/management/cpa-reductant/ui` | 服务端渲染 HTML 看板 | `?refresh=5`、`?events=N`、`?action=...` |
| `GET /v0/management/cpa-reductant/status` | JSON 状态与统计快照 | `?format=html`、`?events=N`、`?action=mask\|warn\|block\|restore\|passthrough\|error` |
| `GET /v0/management/cpa-reductant/metrics` | Prometheus 指标（`0.0.4` 文本格式） | — |
| `POST /v0/management/cpa-reductant/preview` | 只读预览脱敏效果 | `{"text": "...", "mode": "text\|json", "protocol": "...", "model": "...", "override": {...}}` |
| `GET /v0/management/cpa-reductant/debug/capture` | 查询内存调试抓包 | `?id=`、`?limit=`、`?protocol=`、`?model=` |
| `POST /v0/management/cpa-reductant/debug/capture` | 开启/关闭或更新抓包 TTL | `{"enabled": true, "ttl_seconds": 300}` |
| `DELETE /v0/management/cpa-reductant/debug/capture` | 清空全部抓包记录 | — |
| `POST /v0/management/cpa-reductant/model/download` | 触发或重试本地模型下载 | `{"mirror": "..."}`（可选） |
