[English](../en/local-model.md) | **简体中文**

# 本地 PII 语义识别模型（AstrLink Guard）

内建规则适合密钥、邮箱、手机号等强格式内容；如果需要进一步识别**人名、地址、机构名**等非结构化个人隐私，可以开启内嵌的轻量本地模型 `astrlink-guard`（约 39 MB int8 量化 BERT）。

由于采用纯 CPU / macOS CoreML 本地推理，推理速度可能较慢，长文本场景会受 `wait_ms` 超时约束，建议按需开启。通用配置项请参阅 [配置说明](configuration.md)，或返回 [README](README.md)。

## 1. 在线自动启用（最小配置）

```yaml
model:
  enabled: true
```

启用后插件会在后台异步拉取模型权重（优先 `huggingface.co`，失败自动回退 `hf-mirror.com`），支持：

- `HTTP_PROXY` / `HTTPS_PROXY` 环境变量代理
- 断点续传
- SHA-256 完整性校验

下载全程异步进行，CPA 启动后即可立即处理正常请求。

## 2. 模型存放路径（`model.dir`）

目录解析优先级如下：

1. YAML 中的 `model.dir`
2. 环境变量 `CPA_REDUCTANT_MODEL_DIR`
3. 各平台默认缓存目录：
   - **Linux**：`$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard`（未设置 `$XDG_CACHE_HOME` 时为 `~/.cache/cpa-reductant/models/astrlink-guard`）
   - **macOS**：`~/Library/Caches/cpa-reductant/models/astrlink-guard`
   - **Windows**：`%LocalAppData%\cpa-reductant\models\astrlink-guard`

> 如果 CPA 作为 `systemd` 服务或无 `HOME` 的容器用户运行，建议显式配置 `model.dir` 或 `CPA_REDUCTANT_MODEL_DIR`。

## 3. 离线手动部署

如果运行环境无法访问外网，可前往 [QuantumNous/astrlink-guard](https://huggingface.co/QuantumNous/astrlink-guard/tree/main/int8)（或镜像 [hf-mirror.com](https://hf-mirror.com/QuantumNous/astrlink-guard/tree/main/int8)）下载 `int8/` 下的 `model_int8.onnx`、`model_int8.onnx_data` 和 `tokenizer.json` 三个文件，直接平铺放入模型目录，并将 `auto_download` 设为 `false`。

也可在管理看板或 `GET /v0/management/cpa-reductant/status` 的 `model.revision` 字段查看当前插件锁定的 revision，使用 `curl` 在可联网的机器上拉取后拷贝至目标目录：

```sh
MODEL_DIR="/path/to/astrlink-guard"
REV="main" # 或替换为 /v0/management/cpa-reductant/status 返回的 model.revision
BASE="https://huggingface.co/QuantumNous/astrlink-guard/resolve/${REV}/int8" # 国内可换为 hf-mirror.com
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

## 4. 模型参数与运行特性

- **`min_score`**：置信度阈值，范围 `[0.5, 1.0)`，默认 `0.8`。越低越敏感，越高越保守。
- **`strict`**：默认 `false`。
  - `false`（推荐）：模型未就绪或单次等待超时时，保留已扫描片段并自动降级为规则脱敏；
  - `true`：模型缺失或损坏时直接返回 HTTP `503`（不触发 CPA 上游凭据冷却）；若长会话持续因新文本超时，会临时收敛等待时间至 `2000ms` 防止请求雪崩。
- **`wait_ms`**：单次请求等待模型推理的上限毫秒数（默认：非严格模式 `2000`，严格模式 `15000`）。
- **`mirror`**：自定义下载镜像地址（留空时 `huggingface.co` 失败自动回退 `hf-mirror.com`）。
- **运行方式与缓存**：纯进程内 CPU / macOS CoreML 推理，随插件进程直接运行；支持 4 KB 稳定分块与进程内 LRU / 磁盘特征缓存（`<dir>/found-<rev>.bin` 仅保存哈希与偏移量，只存特征索引、不落盘原文）。

## 5. 远程 HTTP NER 后端（`provider: http`）

如需将语义 PII 识别卸载到独立 GPU 服务或外部 NER 引擎（如 Microsoft Presidio 或自建 GLiNER 服务），可将 `model.provider` 设为 `http`。远程模式自动复用相同的 4 KB 分块、内存 LRU 缓存、并发去重、`wait_ms` 超时控制与 `strict` 降级机制：

```yaml
model:
  enabled: true
  provider: http
  format: presidio              # generic（默认）| presidio
  endpoint: http://127.0.0.1:5001/analyze
  language: en                  # 传给 Presidio analyzer 的语言参数（默认 en）
  min_score: 0.8
  wait_ms: 2000
  headers:
    Authorization: "Bearer <optional-token>"
```

- **`generic` 格式**：`POST` `{"text": "..."}`，期望返回 `{"spans": [{"start": 0, "end": 4, "label": "private_person", "score": 0.95}]}`（UTF-8 字节偏移，也兼容顶层 JSON 数组或 `results` 字段）。
- **`presidio` 格式**：`POST` `{"text": "...", "language": "...", "score_threshold": 0.8}`，期望返回 Presidio JSON 数组 `[{"start": 0, "end": 4, "entity_type": "PERSON", "score": 0.95}]`（插件会自动将字符/rune 偏移转换为 UTF-8 字节偏移）。
