# sherpa-tts

中文语音合成网关（Go）。一个容器同时提供：

- **本地模型**：[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) + Kokoro / MeloTTS，纯 CPU，免费、离线、数据不出内网；内置为提供商 `local`。
- **第三方 TTS**：在配置里填服务地址和密钥即可调用——OpenAI 兼容（含硅基流动、Kokoro-FastAPI、openedai-speech 等）、Azure 语音、阿里云百炼（CosyVoice / Qwen-TTS）、火山引擎豆包。

所有提供商的音色合并成一个列表，按「提供商:音色」合成，结果按音色 + 文字缓存。配置接口由本服务提供，各项目的后端转发并做权限校验，前端各自实现配置页面。[MediaVault](https://github.com/haiboyuwen/MediaVault)（小说朗读）和 [zuji](https://github.com/haiboyuwen/zuji)（族谱朗读）都这样接入。

```
ghcr.io/haiboyuwen/sherpa-tts:latest   # linux/amd64, linux/arm64
```

## 运行

```bash
docker run -d --name tts -p 127.0.0.1:8000:8000 \
  -e TTS_API_TOKEN=change-me \
  -v tts-models:/models -v tts-data:/data \
  ghcr.io/haiboyuwen/sherpa-tts:latest
```

- 本地模型首次启动时从 sherpa-onnx 的 GitHub release 下载到 `/models`（约 0.6GB），下载完成前不出现在音色列表里。只想用第三方时设 `TTS_ENGINES=none`，不会下载模型。
- 第三方配置保存在 `/data/config.json`（0600），合成缓存在 `/data/cache`。
- 设置 `TTS_API_TOKEN` 后，`/v1/*` 需要 `Authorization: Bearer <token>`。它是给应用后端用的服务间令牌，不要把本服务直接暴露给浏览器。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/voices` | `{available, voices:[{id, label, provider, provider_name}], default_voice}`；`id` 如 `local:melo:0`、`azure:zh-CN-XiaoxiaoNeural` |
| POST | `/v1/synthesize` | `{voice, text}` → 音频（`audio/mpeg`，Qwen-TTS 为 `audio/wav`）。`text` ≤ 600 字（413）；音色未知 400；无可用提供商 503；上游失败 502 |
| GET | `/v1/config` | `{config:{providers, default_voice}, types, voices, local:{enabled, engines}, settings_storage}`；密钥只给 `has_api_key` / `api_key_hint` |
| PUT | `/v1/config` | `{providers:[…], default_voice}` 整体替换第三方配置并落盘；同 ID 的 `api_key` 留空 = 保留，`clear_api_key: true` = 删除；校验失败 400 |
| POST | `/v1/config/test` | `{provider, voice?, text?}` 用未保存的草稿试听一句；成功返回音频，失败 502 `{error}` |
| GET | `/health` | 本地引擎状态（不需要令牌，供 Docker 健康检查） |
| POST | `/synthesize` | 旧接口，`{engine, voice, text}` 只合成本地引擎，兼容老客户端 |

`types` 描述每种第三方类型的表单字段（`fields`：key / label / required / secret / placeholder / help）、默认音色和文档链接，配置页照着渲染即可，新增提供商不用改前端。旧客户端存的 `melo:0` 这类音色会自动路由到本地模型。

### 第三方提供商

| 类型 | 必填 | 说明 |
| --- | --- | --- |
| `openai` | — | `POST {base_url}/audio/speech`，默认 `https://api.openai.com/v1`、`gpt-4o-mini-tts`；可填朗读风格 `instructions` |
| `azure` | 密钥 + 区域或端点 | SSML 合成；不填音色时在线列出全部中文音色 |
| `dashscope` | API Key | `cosyvoice-*`（默认 `cosyvoice-v2`）走 WebSocket，`qwen-tts` / `qwen3-tts-*` 走 HTTP |
| `volcengine` | App ID + Access Token | 豆包语音合成 HTTP v1，集群默认 `volcano_tts` |

音色列表每行一个 `音色ID=显示名`，不填则用默认音色。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TTS_PORT` | `8000` | 监听端口 |
| `TTS_API_TOKEN` | 空 | 设置后 `/v1/*` 需要 Bearer 令牌 |
| `TTS_MODEL_DIR` | `/models` | 本地模型目录 |
| `TTS_DATA_DIR` | `/data` | 配置（`config.json`）与缓存目录 |
| `TTS_ENGINES` | `melo,kokoro` | 本地引擎；`none` = 不启用 |
| `TTS_THREADS` | `4` | 每个本地引擎的推理线程数 |
| `TTS_MODEL_MIRROR` | sherpa-onnx `tts-models` release | 模型下载源 |

`config.json` 不存在时，第三方配置从环境变量初始化（保存一次后以文件为准）：

```
TTS_OPENAI_API_KEY / _BASE_URL / _MODEL / _VOICES / _INSTRUCTIONS
TTS_AZURE_API_KEY / _REGION / _BASE_URL / _VOICES
TTS_DASHSCOPE_API_KEY / _MODEL / _BASE_URL / _VOICES
TTS_VOLCENGINE_APP_ID / _ACCESS_TOKEN / _CLUSTER / _VOICES
TTS_DEFAULT_VOICE                       如 azure:zh-CN-XiaoxiaoNeural
```

## 开发

需要 CGO（sherpa-onnx 预编译库 + libmp3lame）。macOS：`brew install lame && go build && go test ./...`。第三方提供商在 `internal/tts`（纯 Go），每种类型一个文件，加新类型时在 `types.go` 里补表单元数据。

推到 `main`：CI 跑测试，再构建 linux/amd64 + linux/arm64 镜像推送 `latest` 与 `sha-<hash>`；打 `v*` tag 额外推送版本号。

模型各自遵循其上游许可证（Kokoro: Apache-2.0，MeloTTS: MIT）。本仓库代码为 MIT。
