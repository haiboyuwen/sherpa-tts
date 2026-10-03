# sherpa-tts

中文语音合成的两件套，都用 Go 写：

- **本地 TTS 服务**（`server/`，Docker 镜像 `ghcr.io/haiboyuwen/sherpa-tts`）：[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) + Kokoro / MeloTTS，纯 CPU 推理，输出 mp3。免费、离线、数据不出内网。
- **多提供商客户端库**（`tts/`，`github.com/haiboyuwen/sherpa-tts/tts`）：统一接入本地服务和第三方 TTS，带音色汇总、按音色路由、磁盘缓存、并发控制、后台配置所需的脱敏/合并/试听和表单元数据。纯 Go，无 CGO。

[MediaVault](https://github.com/haiboyuwen/MediaVault)（小说朗读）和 [zuji](https://github.com/haiboyuwen/zuji)（族谱朗读）都用这个库，自己只做路由接线和配置存储。

## 客户端库

| 类型 | 说明 | 必填 |
| --- | --- | --- |
| `sherpa` | 本地 sherpa-tts 服务，音色自动从 `/health` 读取 | 服务地址 |
| `openai` | OpenAI `/v1/audio/speech`；硅基流动、Kokoro-FastAPI、openedai-speech 等兼容服务改地址/模型/音色即可 | — |
| `azure` | Azure AI Speech REST（SSML）；不填音色时在线列出全部中文音色 | 密钥 + 区域或端点 |
| `dashscope` | 阿里云百炼：`cosyvoice-*` 走 WebSocket，`qwen-tts` / `qwen3-tts-*` 走 HTTP | API Key |
| `volcengine` | 火山引擎豆包语音合成 HTTP v1 | App ID + Access Token |

```go
import "github.com/haiboyuwen/sherpa-tts/tts"

cfg, ok, _ := tts.LoadFile("config/tts-settings.json") // 后台保存的配置（0600）
if !ok {
	cfg = tts.ConfigFromEnv("TTS_", nil) // TTS_URL、TTS_OPENAI_API_KEY、TTS_AZURE_API_KEY …
}
svc, _ := tts.New(cfg, tts.Options{CacheDir: "data/tts-cache"})

mux.Handle("GET /api/tts/voices", svc.VoicesHandler())  // {available, voices:[{id:"azure:zh-CN-XiaoxiaoNeural", label, provider, provider_name}], default_voice}
mux.Handle("POST /api/tts", svc.SynthesizeHandler())   // {voice, text} -> audio/mpeg 或 audio/wav
```

- 音色 ID 是「提供商ID:音色ID」，如 `local:melo:0`、`openai:nova`。旧版只存了 `melo:0` 的客户端会自动路由到第一个 sherpa 提供商。
- 单段 ≤ 600 字；同一段并发请求只合成一次；结果按「提供商 + 模型 + 音色 + 文字」缓存，超过容量（默认 512MB）按最近使用淘汰。语速建议用播放端 `playbackRate` 调，不重新合成、不重复计费。
- 后台配置页：`Config.View()` 返回不含密钥明文的配置（只有 `has_api_key` / `api_key_hint`）；`MergeUpdate` 处理「密钥留空 = 保留，`clear_api_key` = 删除」；`TestProvider` 用表单配置合成一句试听；`ProviderTypes()` 给出每种类型的表单字段、说明和默认音色，前端照着渲染即可，新增提供商不用改前端。

环境变量（`ConfigFromEnv(prefix, …)`，`*_VOICES` 为逗号分隔的 `音色ID=显示名`）：

```
{prefix}URL                                   本地 sherpa-tts → 提供商 local
{prefix}OPENAI_API_KEY / _BASE_URL / _MODEL / _VOICES / _INSTRUCTIONS
{prefix}AZURE_API_KEY / _REGION / _BASE_URL / _VOICES
{prefix}DASHSCOPE_API_KEY / _MODEL / _BASE_URL / _VOICES
{prefix}VOLCENGINE_APP_ID / _ACCESS_TOKEN / _CLUSTER / _VOICES
{prefix}DEFAULT_VOICE                         如 azure:zh-CN-XiaoxiaoNeural
```

## 本地 TTS 服务

```bash
docker run -d --name tts -p 127.0.0.1:8000:8000 -v tts-models:/models ghcr.io/haiboyuwen/sherpa-tts:latest
```

模型不进镜像。首次启动时从 sherpa-onnx 的 GitHub release 下载到 `/models`（约 0.6GB），挂卷即可持久化；下载完成前对应引擎在 `/health` 里是 `ready: false`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | `{"engines": {"kokoro": {"label", "ready", "error", "voices": [{"id", "label"}]}, "melo": {...}}}` |
| POST | `/synthesize` | `{"engine": "kokoro"\|"melo", "voice": <speaker id>, "text": "..."}` → `audio/mpeg`（64kbps 单声道） |

`text` 最多 600 字（413）；引擎未知 400；模型未就绪 503；合成失败 500。弯引号、书名号、括号等模型词表不认的标点会先压成普通逗号/句号。同一引擎串行推理。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TTS_PORT` | `8000` | 监听端口 |
| `TTS_MODEL_DIR` | `/models` | 模型目录 |
| `TTS_ENGINES` | `melo,kokoro` | 启用的引擎 |
| `TTS_THREADS` | `4` | 每个引擎的推理线程数 |
| `TTS_MODEL_MIRROR` | sherpa-onnx `tts-models` release | 模型下载源，国内可换镜像 |

当前音色：`kokoro:3`（Kokoro 中文）、`melo:0`（MeloTTS 中文）；增删音色改 `server/engines.go` 的 `specs`。

服务需要 CGO（sherpa-onnx 预编译库 + libmp3lame），所以单独一个 Go 模块（`server/go.mod`），不会被库的使用方引入。本机开发：macOS `brew install lame` 后 `cd server && go build`。

## 发布

推到 `main`：CI 跑库测试，再构建 linux/amd64 + linux/arm64 镜像推送 `latest` 与 `sha-<hash>`；打 `v*` tag 额外推送对应版本号，同时就是 Go 库的版本。

模型各自遵循其上游许可证（Kokoro: Apache-2.0，MeloTTS: MIT）。本仓库代码为 MIT。
