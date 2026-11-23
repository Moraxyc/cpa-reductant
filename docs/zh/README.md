[English](../../README.md) | **简体中文**

# CPA Reductant

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）的可逆隐私脱敏插件。将插件放入 CPA 插件目录并启用后，发往上游大模型的密钥、个人隐私与自定义敏感词会自动替换为占位符，并在模型返回时自动还原为原文（同时支持 JSON 与流式 SSE）。

## 工作原理

```text
客户端 ──▶ CLIProxyAPI + cpa-reductant（脱敏 / 告警 / 拦截）──▶ 上游模型
客户端 ◀── CLIProxyAPI + cpa-reductant（还原，支持 JSON 与 SSE）◀── 上游模型
```

- **可逆脱敏与还原**：请求出站时将敏感内容替换为确定性 HMAC 占位符（或可选保形伪造值），模型响应（含流式 SSE）返回时自动还原原文。
- **多层检测引擎**：结合内建密钥与强格式隐私规则（默认关闭 `home`、`userhost`、`ip`、`bucket`，避免误伤编程路径与常见网络地址）、自定义词表/前缀/正则规则，以及可选的本地 PII 语义模型（`astrlink-guard`）。
- **零明文可观测性**：内置鉴权管理看板，提供实时统计、在线试跑预览与临时内存调试抓包，审计与日志均不落盘明文。
- **多协议覆盖**：支持 `claude`、`openai`、`openai-response`（支持 WebSocket 客户端接入）、`gemini`、`interactions`、`openai-image` / `openai-speech` / `openai-video`、`codex` 与 `antigravity`。

## 安装与快速开始

1. **获取插件**：
   - **预编译产物**：前往 [Latest Release](https://github.com/Moraxyc/cpa-reductant/releases/latest) 下载对应平台的 `cpa-reductant_<version>_<os>_<arch>.zip`（覆盖 `linux`/`darwin`/`windows` × `amd64`/`arm64`，可用 [checksums.txt](https://github.com/Moraxyc/cpa-reductant/releases/latest/download/checksums.txt) 校验 SHA-256），将解压后的 `.so` / `.dylib` / `.dll` 放入 CPA 的 `plugins.dir` 目录。
   - **Nix / NixOS**：使用 Flake 导出的 `packages.<system>.cpa-reductant` 或 `overlays.default`（提供 `pkgs.cpa-reductant`），产物位于 `$out/lib/cliproxyapi/plugins/<os>/<arch>/cpa-reductant.<ext>`。

2. **在 `config.yaml` 中启用**并启动或重载 CPA：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-reductant:
      enabled: true
```

默认配置下会自动保护所有已配置的上游模型及支持的协议格式，并开启常见密钥与个人隐私规则。

## 文档导航

| 主题 | 文档 | 内容说明 |
| --- | --- | --- |
| 配置说明 | [配置参考](configuration.md) | 作用范围（`models` 与 `model`）、`kinds` 类别、`words`/`rules`/`allow`、`actions`、`shape`、`key_path` 持久化、协议范围与限制 |
| 语义识别 | [本地 PII 模型（AstrLink Guard）](local-model.md) | 在线自动拉取、`model.dir` 优先级、离线手动部署、`min_score`/`strict`/`wait_ms` 与缓存机制 |
| 运维监控 | [管理面板与运维接口](management.md) | Web 管理看板、脱敏预览、调试抓包、模型下载控制与 HTTP 接口清单 |
| 开发贡献 | [开发者指南](DEVELOPMENT.md) | 架构原理、Magpie 补丁、Nix 与原生构建、测试、CI/发布与上游升级清单 |

## Credit

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)：提供原生插件 ABI 宿主与多协议代理底座
- [Magpie](https://github.com/yetone/magpie)：提供核心可逆脱敏引擎（`redact`）与本地模型推理封装（`guard`）
- [AstrLink Guard](https://huggingface.co/QuantumNous/astrlink-guard)：提供轻量级本地 PII 语义识别模型

## License

本项目基于 [MIT License](../../LICENSE) 开源。
