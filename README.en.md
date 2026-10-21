# sherpa-tts

Chinese text-to-speech gateway (Go). Single container providing:

- **Local models**: [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) + Kokoro / MeloTTS, pure CPU, free, offline, data stays in-network; built-in as provider `local`.
- **Third-party TTS**: Fill in service endpoint and credentials to call—OpenAI-compatible (including SiliconFlow, Kokoro-FastAPI, openedai-speech, etc.), Azure Speech, Alibaba Cloud DashScope (CosyVoice / Qwen-TTS), Volcengine Doubao.

All providers' voices merge into one list, synthesized by `provider:voice`, cached by voice + text. Configuration interface provided by this service; each project's backend forwards and handles auth, with frontend config pages implemented separately. [MediaVault](https://github.com/haiboyuwen/MediaVault) (novel reading) and [zuji](https://github.com/haiboyuwen/zuji) (genealogy reading) integrate this way.

```
ghcr.io/haiboyuwen/sherpa-tts:latest   # linux/amd64, linux/arm64
```

## Running

```bash
docker run -d --name tts -p 127.0.0.1:8000:8000 \
  -e TTS_API_TOKEN=change-me \
  -v tts-models:/models -v tts-data:/data \
  ghcr.io/haiboyuwen/sherpa-tts:latest
```

- Local models download from sherpa-onnx GitHub releases to `/models` on first start (~0.6GB); voices won't appear until download completes. Set `TTS_ENGINES=none` to skip model download when using only third-party providers.
- Third-party config saved to `/data/config.json` (0600), synthesis cached in `/data/cache`.
- After setting `TTS_API_TOKEN`, `/v1/*` requires `Authorization: Bearer <token>`. This is a service-to-service token for app backends—don't expose this service directly to browsers.

## API

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/voices` | `{available, voices:[{id, label, provider, provider_name}], default_voice}`; `id` like `local:melo:0`, `azure:zh-CN-XiaoxiaoNeural` |
| POST | `/v1/synthesize` | `{voice, text}` → audio (`audio/mpeg`, Qwen-TTS returns `audio/wav`). `text` ≤ 600 chars (413); unknown voice 400; no provider available 503; upstream failure 502 |
| GET | `/v1/config` | `{config:{providers, default_voice}, types, voices, local:{enabled, engines}, settings_storage}`; keys only expose `has_api_key` / `api_key_hint` |
| PUT | `/v1/config` | `{providers:[…], default_voice}` replaces third-party config and persists; leaving `api_key` empty = keep existing, `clear_api_key: true` = delete; validation failure 400 |
| POST | `/v1/config/test` | `{provider, voice?, text?}` test with unsaved draft; returns audio on success, 502 `{error}` on failure |
| GET | `/health` | Local engine status (no token required, for Docker health checks) |
| POST | `/synthesize` | Legacy endpoint, `{engine, voice, text}` synthesizes local engine only, backward compatible with old clients |

`types` describes form fields for each third-party provider (`fields`: key / label / required / secret / placeholder / help), default voice, and doc links—render config pages accordingly, no frontend changes needed when adding providers. Legacy voice IDs like `melo:0` auto-route to local models.

Local voices: MeloTTS 1 voice (`local:melo:0`).

**ZipVoice Cloning** (`local:zipvoice:<name>`): Zero-shot voice cloning model with higher voice quality and prosody ceiling than Kokoro, slower on CPU (few seconds per sentence). Voice = reference audio: create `voices/` directory in data volume, each voice needs a pair of files—`<name>.wav` (16-bit PCM, 5–10 seconds of clean single-speaker reading) and `<name>.txt` (reference text matching audio word-for-word, ≤600 char limit applies to synthesis text too). Built-in `local:zipvoice:leijun` example ready to test. GPU-independent, data stays in-network; Apache-2.0 license.

### Third-Party Providers

| Type | Required | Description |
| --- | --- | --- |
| `openai` | — | `POST {base_url}/audio/speech`, defaults to `https://api.openai.com/v1`, `gpt-4o-mini-tts`; optional `instructions` for reading style |
| `azure` | Key + region or endpoint | SSML synthesis; lists all Chinese voices online when voice not specified |
| `dashscope` | API Key | `cosyvoice-*` (default `cosyvoice-v2`) via WebSocket, `qwen-tts` / `qwen3-tts-*` via HTTP |
| `volcengine` | App ID + Access Token | Doubao TTS HTTP v1, cluster defaults to `volcano_tts` |

Voice list is one `voice_id=display_name` per line, uses default voice when empty.

## Environment Variables

| Variable | Default | Description |
| --- | --- | --- |
| `TTS_PORT` | `8000` | Listen port |
| `TTS_API_TOKEN` | empty | `/v1/*` requires Bearer token when set |
| `TTS_MODEL_DIR` | `/models` | Local model directory |
| `TTS_DATA_DIR` | `/data` | Config (`config.json`) and cache directory |
| `TTS_ENGINES` | `melo,zipvoice` | Local engines; `none` = disabled |
| `TTS_THREADS` | `4` | Inference threads per local engine |
| `TTS_MODEL_MIRROR` | sherpa-onnx `tts-models` release | Model download source |

Third-party config initializes from environment variables when `config.json` doesn't exist (persists after first save):

```
TTS_OPENAI_API_KEY / _BASE_URL / _MODEL / _VOICES / _INSTRUCTIONS
TTS_AZURE_API_KEY / _REGION / _BASE_URL / _VOICES
TTS_DASHSCOPE_API_KEY / _MODEL / _BASE_URL / _VOICES
TTS_VOLCENGINE_APP_ID / _ACCESS_TOKEN / _CLUSTER / _VOICES
TTS_DEFAULT_VOICE                       e.g. azure:zh-CN-XiaoxiaoNeural
```

## Development

Requires CGO (sherpa-onnx precompiled library + libmp3lame). macOS: `brew install lame && go build && go test ./...`. Third-party providers in `internal/tts` (pure Go), one file per type; add form metadata to `types.go` when adding new types.

Pushing to `main`: CI runs tests, builds linux/amd64 + linux/arm64 images and pushes `latest` and `sha-<hash>`; tagging `v*` additionally pushes version number.

Each model follows its upstream license (Kokoro: Apache-2.0, MeloTTS: MIT). Repository code is MIT.
