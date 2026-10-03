# sherpa-tts

本地中文神经网络 TTS 服务：[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) + Kokoro / MeloTTS，纯 CPU 推理，输出 mp3。

它设计成 **sidecar**：和应用放在同一个 Docker 网络里，由应用后端代理、缓存，不直接暴露给浏览器。目前 [MediaVault](https://github.com/haiboyuwen/MediaVault)（小说朗读）和 [zuji](https://github.com/haiboyuwen/zuji)（族谱朗读）都在用。

```
ghcr.io/haiboyuwen/sherpa-tts:latest   # linux/amd64, linux/arm64
```

## 运行

```bash
docker run -d --name tts -p 127.0.0.1:8000:8000 -v tts-models:/models ghcr.io/haiboyuwen/sherpa-tts:latest
```

模型不进镜像。首次启动时从 sherpa-onnx 的 GitHub release 下载到 `/models`（约 0.6GB），挂卷即可持久化；下载完成前对应引擎在 `/health` 里是 `ready: false`。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | `{"engines": {"kokoro": {"label", "ready", "error", "voices": [{"id", "label"}]}, "melo": {...}}}` |
| POST | `/synthesize` | body `{"engine": "kokoro"\|"melo", "voice": <speaker id>, "text": "..."}`，返回 `audio/mpeg`（64kbps 单声道） |

- `text` 最多 600 字，超出返回 413；引擎未知 400；模型未就绪 503；合成失败 500。
- 弯引号、书名号、括号等模型词表不认的标点会先压成普通逗号/句号。
- 同一引擎的请求串行推理；建议调用方按句切成 ≤80 字的短段、向前预取几段，并在应用侧按 `voice + text` 做磁盘缓存。语速用播放端的 `playbackRate` 调，不必重新合成。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TTS_PORT` | `8000` | 监听端口 |
| `TTS_MODEL_DIR` | `/models` | 模型目录 |
| `TTS_ENGINES` | `melo,kokoro` | 启用的引擎，逗号分隔 |
| `TTS_THREADS` | `4` | 每个引擎的推理线程数 |
| `TTS_MODEL_MIRROR` | sherpa-onnx `tts-models` release | 模型下载源，国内可换镜像 |

当前音色：`kokoro:3`（Kokoro 中文）、`melo:0`（MeloTTS 中文）；增删音色改 `server.py` 的 `ENGINES`，`voice` 就是 sherpa-onnx 的 speaker id。

## 发布

推到 `main` 由 GitHub Actions 构建双架构镜像并推送 `latest` 与 `sha-<hash>`；打 `v*` tag 额外推送对应版本号。

模型各自遵循其上游许可证（Kokoro: Apache-2.0，MeloTTS: MIT）。本仓库代码为 MIT。
