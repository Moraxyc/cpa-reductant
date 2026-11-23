[English](../../README.md) | **简体中文**

# CPA Reductant

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）的可逆脱敏插件。请求发往上游大模型前替换密钥、个人隐私和自定义敏感词，响应返回时（支持 JSON 与流式 SSE）再还原回原文。

```text
客户端 ──▶ CLIProxyAPI + cpa-reductant（脱敏 / 告警 / 拦截）──▶ 上游模型
客户端 ◀── CLIProxyAPI + cpa-reductant（还原，JSON & SSE）◀── 上游模型
```

## 特性

- 请求出站时将命中内容替换为确定性 HMAC 占位符（或保形伪造值），响应返回时按占位符还原。
- 内建密钥与常见个人隐私规则（`home`、`userhost`、`ip`、`bucket` 默认关闭，减少对本地路径和网络地址的误判），支持自定义词表、前缀/正则规则，以及可选的 PII 模型（本地 `astrlink-guard` 或远程 HTTP / Presidio NER）。
- 支持按模型/协议分级策略、基于 `CallerScope` 的租户还原隔离、异步 Webhook 告警、Prometheus `/metrics`，以及自带预览与临时抓包的管理面板。
- 覆盖 `claude`、`openai`、`openai-response`（含 WebSocket 客户端接入）、`gemini`、`interactions`、`openai-image` / `openai-speech` / `openai-video`、`codex` 与 `antigravity`。

## 安装与启用

### 预编译产物

从 [Releases](https://github.com/Moraxyc/cpa-reductant/releases/latest) 下载 `cpa-reductant_<version>_<os>_<arch>.zip`（覆盖 `linux`/`darwin`/`windows` × `amd64`/`arm64`，校验和见 [checksums.txt](https://github.com/Moraxyc/cpa-reductant/releases/latest/download/checksums.txt)），解压出 `.so` / `.dylib` / `.dll` 放到 CPA 的 `plugins.dir`。

### Nix

引用 Flake 导出的 `packages.<system>.cpa-reductant` 或 `overlays.default`（`pkgs.cpa-reductant`），动态库位于 `$out/lib/cliproxyapi/plugins/<os>/<arch>/cpa-reductant.<ext>`。

### 在 `config.yaml` 中启用

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
```

启动或重载 CPA 后生效。默认保护所有已配置模型和受支持协议，并开启内建密钥与个人隐私规则。

## 文档

- [配置参考](configuration.md) — 模型通配符、`policies` 分级策略、规则选项、`scope_isolation`、`alerts` 与限制
- [PII 模型指南](local-model.md) — 本地 `astrlink-guard` 部署、离线权重与远程 HTTP / Presidio NER
- [管理面板与接口](management.md) — Web 面板、预览试跑、调试抓包、Prometheus `/metrics` 与 HTTP 接口
- [开发者指南](DEVELOPMENT.md) — 架构、Magpie 补丁、构建、测试与上游同步

## 致谢

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — 原生插件 ABI 宿主与多协议代理
- [Magpie](https://github.com/yetone/magpie) — 可逆脱敏（`redact`）与模型封装（`guard`）
- [AstrLink Guard](https://huggingface.co/QuantumNous/astrlink-guard) — 本地 int8 PII 识别模型

## 许可证

[MIT](../../LICENSE)
