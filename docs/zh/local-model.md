[English](../en/local-model.md) | **简体中文**

# PII 识别模型

内建规则覆盖密钥、邮箱、手机号等结构化内容。如果需要进一步识别人名、地址、机构名等非结构化隐私，可以启用内嵌本地模型 `astrlink-guard`（约 39 MB int8 BERT，CPU / macOS CoreML），或接入远程 HTTP NER 服务。

本地 CPU 推理在长文本下较慢且受 `wait_ms` 约束，不需要非结构化 PII 识别时保持关闭即可。

## 1. 本地模型自动下载

```yaml
model:
  enabled: true
```

启用后在后台异步下载权重（优先 `huggingface.co`，失败回退 `hf-mirror.com`），支持 `HTTP_PROXY`/`HTTPS_PROXY`、断点续传和 SHA-256 校验，不阻塞 CPA 启动与请求处理。

## 2. 模型目录（`model.dir`）

目录解析优先级：

1. `config.yaml` 中的 `model.dir`
2. 环境变量 `CPA_REDUCTANT_MODEL_DIR`
3. 平台默认缓存目录：
   - Linux：`$XDG_CACHE_HOME/cpa-reductant/models/astrlink-guard`（未设置时为 `~/.cache/...`）
   - macOS：`~/Library/Caches/cpa-reductant/models/astrlink-guard`
   - Windows：`%LocalAppData%\cpa-reductant\models\astrlink-guard`

在 `systemd` 服务或没有 `HOME` 的容器环境中，建议显式指定 `model.dir` 或 `CPA_REDUCTANT_MODEL_DIR`。

## 3. 离线部署

从 [QuantumNous/astrlink-guard](https://huggingface.co/QuantumNous/astrlink-guard/tree/main/int8)（或 [hf-mirror.com](https://hf-mirror.com/QuantumNous/astrlink-guard/tree/main/int8)）下载 `int8/` 下的 `model_int8.onnx`、`model_int8.onnx_data` 和 `tokenizer.json` 放到模型目录，并将 `auto_download` 设为 `false`。

锁定的 revision 可从 `GET /v0/management/cpa-reductant/status` 的 `model.revision` 读取：

```sh
MODEL_DIR="/path/to/astrlink-guard"
REV="main" # 或替换为 /v0/management/cpa-reductant/status 中的 model.revision
BASE="https://huggingface.co/QuantumNous/astrlink-guard/resolve/${REV}/int8" # 或 hf-mirror.com
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

## 4. 运行参数

- `min_score`：置信度阈值，范围 `[0.5, 1.0)`，默认 `0.8`。
- `strict`：默认 `false`。
  - `false`：模型未就绪或超时时，保留已扫描片段，其余回退到规则脱敏。
  - `true`：权重缺失或损坏时返回 HTTP `503`（不触发 CPA 上游凭据冷却）。连续超时后，单次等待上限会临时收紧到 `2000ms`，待推理恢复后解除。
- `wait_ms`：单次请求等待模型推理的毫秒上限（默认：非严格模式 `2000`，严格模式 `15000`）。
- `mirror`：自定义下载镜像（留空时按 `huggingface.co` → `hf-mirror.com` 回退）。
- 缓存：文本按 4 KB 分块，结合内存 LRU 与磁盘缓存（`<dir>/found-<rev>.bin`，仅保存哈希与字节偏移，不存原文）。

## 5. 远程 HTTP NER（`provider: http`）

如需将语义识别交给独立 GPU 服务或外部 NER 引擎（如 Microsoft Presidio、自建 GLiNER），将 `model.provider` 设为 `http`。远程模式沿用相同的 4 KB 分块、内存 LRU、`wait_ms` 超时与 `strict` 降级逻辑：

```yaml
model:
  enabled: true
  provider: http
  format: presidio              # generic（默认）| presidio
  endpoint: http://127.0.0.1:5001/analyze
  language: en                  # 传给 Presidio 的语言参数（默认 en）
  min_score: 0.8
  wait_ms: 2000
  headers:
    Authorization: "Bearer <optional-token>"
```

- `generic`：`POST {"text": "..."}`，响应格式为 `{"spans": [{"start": 0, "end": 4, "label": "private_person", "score": 0.95}]}`（UTF-8 字节偏移；兼容顶层数组与 `results` 字段）。
- `presidio`：`POST {"text": "...", "language": "...", "score_threshold": 0.8}`，响应为 `[{"start": 0, "end": 4, "entity_type": "PERSON", "score": 0.95}]`；字符偏移会自动换算为 UTF-8 字节偏移。
