[English](../en/management.md) | **简体中文**

# 管理面板与运维接口

`cpa-reductant` 内置可视化管理看板与鉴权 HTTP 接口，用于查看脱敏统计、在线试跑规则、排查请求对照以及管理本地模型权重。返回 [README](README.md) 查看项目概览与安装说明。

## 打开可视化面板

1. 打开 CPA 管理后台页面（`management.html`），在左侧「插件」菜单中点击 `cpa-reductant`（或直接访问 `/v0/resource/plugins/cpa-reductant/ui`）。
2. 输入 `config.yaml` 中配置的 `management.secret-key` 登录（凭证仅保存于当前浏览器标签页）。

## 面板常用功能

- **查看运行概览**：实时展示当前生效策略、脱敏/告警/拦截/还原次数、平均耗时、各协议与模型流量分布，以及近期审计事件记录（仅展示脱敏后信息与类型，不记录明文）。
- **脱敏预览（在线测试）**：支持纯文本或完整 API JSON 请求体（`mode: "json"`），可指定目标 `model`/`protocol` 匹配分路由策略，或传入临时 `override`（如追加 `words`、`allow`、开启 `shape`）做 What-If 试跑，不影响统计数据也不保存映射记录。
- **调试抓包（临时对照）**：遇到规则误伤或还原异常时，可在面板中临时开启内存抓包（默认 5 分钟自动过期、最长 30 分钟），直接对照查看「原始请求 / 脱敏请求 → 上游 / 上游响应 / 还原响应 → 客户端」，支持行内命中高亮、一键导出抓包 JSON 及一键复制 `allow` YAML 规则片段，排查完毕可随时关闭并清空缓存。
- **管理本地模型**：查看 `astrlink-guard` 权重状态、解析路径与下载进度；如后台拉取失败，可在面板中一键触发或重试下载。

> 所有拦截、降级与模型下载事件也会同步写入 CPA 宿主日志。

## 自动化运维 / HTTP 接口

以下接口均需携带 `management.secret-key` 鉴权（例如通过 `Authorization: Bearer <secret-key>` 或 `X-Management-Key: <secret-key>`）。

| 接口 | 用途 | 常用参数 / 请求体 |
| --- | --- | --- |
| `GET /v0/management/cpa-reductant/ui` | 服务端渲染 HTML 监控看板 | `?refresh=5`（自动刷新秒数）、`?events=N`、`?action=...` |
| `GET /v0/management/cpa-reductant/status` | 获取 JSON 全量状态与统计快照 | `?format=html`、`?events=N`、`?action=mask\|warn\|block\|restore\|passthrough\|error` |
| `GET /v0/management/cpa-reductant/metrics` | 导出 Prometheus 文本格式指标（`0.0.4`） | 无 |
| `POST /v0/management/cpa-reductant/preview` | 只读测试脱敏效果（支持文本 / JSON 与临时策略覆写） | `{"text": "...", "mode": "text\|json", "protocol": "...", "model": "...", "override": {...}}` |
| `GET /v0/management/cpa-reductant/debug/capture` | 读取当前内存调试抓包记录 | `?id=`、`?limit=`、`?protocol=`、`?model=` |
| `POST /v0/management/cpa-reductant/debug/capture` | 开启/关闭或重置抓包 TTL | `{"enabled": true, "ttl_seconds": 300}` |
| `DELETE /v0/management/cpa-reductant/debug/capture` | 立即清空全部抓包缓存 | 无 |
| `POST /v0/management/cpa-reductant/model/download` | 手动触发或重试本地模型下载 | `{"mirror": "..."}`（可选） |
