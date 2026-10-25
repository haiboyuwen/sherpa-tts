# sherpa-tts

Chinese text-to-speech gateway (Go). Single container providing:

- **Local voices**: By default, [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) + ZipVoice uses the voices in `voices/`, running offline on CPU without cloud credentials; built-in as provider `local`.
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
  -v "$PWD/voices:/data/voices:ro" \
  ghcr.io/haiboyuwen/sherpa-tts:latest
```

- By default, only the ZipVoice model and vocoder are downloaded to `/models`; directory voices appear once the model is ready. Set `TTS_ENGINES=none` to disable local models.
- Third-party config saved to `/data/config.json` (0600), synthesis cached in `/data/cache`.
- After setting `TTS_API_TOKEN`, `/v1/*` requires `Authorization: Bearer <token>`. This is a service-to-service token for app backends—don't expose this service directly to browsers.

## API

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/voices` | `{available, voices:[{id, label, provider, provider_name}], default_voice}`; `id` like `local:zipvoice:晓晓`, `local:zipvoice:云希` |
| POST | `/v1/synthesize` | `{voice, text}` → audio (`audio/mpeg`, Qwen-TTS returns `audio/wav`). `text` ≤ 600 chars (413); unknown voice 400; no provider available 503; upstream failure 502 |
| GET | `/v1/config` | `{config:{providers, default_voice}, types, voices, local:{enabled, engines}, settings_storage}`; keys only expose `has_api_key` / `api_key_hint` |
| PUT | `/v1/config` | `{providers:[…], default_voice}` replaces third-party config and persists; leaving `api_key` empty = keep existing, `clear_api_key: true` = delete; validation failure 400 |
| POST | `/v1/config/test` | `{provider, voice?, text?}` test with unsaved draft; returns audio on success, 502 `{error}` on failure |
| GET | `/health` | Local engine status (no token required, for Docker health checks) |
| POST | `/synthesize` | Legacy endpoint, `{engine, voice, text}` synthesizes local engine only, backward compatible with old clients |

`types` describes form fields for each third-party provider (`fields`: key / label / required / secret / placeholder / help), default voice, and doc links—render config pages accordingly, no frontend changes needed when adding providers. Legacy voice IDs like `melo:0` auto-route to local models.

Default local voices come only from the directory mounted at `/data/voices`; model-bundle examples are not appended. The repository includes 8 voices: **云健, 云夏, 云希, 云扬, 小北, 小妮, 晓伊, 晓晓**, with IDs such as `local:zipvoice:晓晓`.

**ZipVoice Cloning** (`local:zipvoice:<name>`): Each voice requires a matching `<name>.wav` (16-bit PCM, clean single-speaker recording) and `<name>.txt` (non-empty, matching the recording word-for-word). References must be 1–30 seconds; prefer a complete short sentence. Only pairs passing reference validation are listed. An empty or invalid directory does not fall back to other voices.

Run `docker compose up -d` from the repository to mount `./voices` read-only. Added or removed pairs are discovered on the next query; clients may cache the list for 60 seconds. The image does not contain these assets, so standalone `docker run` also requires the voice mount. Existing deployments with explicit `TTS_ENGINES` or `MEDIAVAULT_TTS_ENGINES` overrides must change them to `zipvoice` or remove the old override; Melo is no longer enabled by default.

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
| `TTS_DATA_DIR` | `/data` | Config (`config.json`), cache and `voices/` directory |
| `TTS_ENGINES` | `zipvoice` | Local engines; `none` = disabled |
| `TTS_THREADS` | `4` | Inference threads per local engine |
| `TTS_MODEL_MIRROR` | sherpa-onnx `tts-models` release | Model download source |

Third-party config initializes from environment variables when `config.json` doesn't exist (persists after first save):

```
TTS_OPENAI_API_KEY / _BASE_URL / _MODEL / _VOICES / _INSTRUCTIONS
TTS_AZURE_API_KEY / _REGION / _BASE_URL / _VOICES
TTS_DASHSCOPE_API_KEY / _MODEL / _BASE_URL / _VOICES
TTS_VOLCENGINE_APP_ID / _ACCESS_TOKEN / _CLUSTER / _VOICES
TTS_DEFAULT_VOICE                       e.g. local:zipvoice:晓晓
```

## Development

Requires CGO (sherpa-onnx precompiled library + libmp3lame). macOS: `brew install lame && go build && go test ./...`. Third-party providers in `internal/tts` (pure Go), one file per type; add form metadata to `types.go` when adding new types.

Pushing to `main`: CI runs tests, builds linux/amd64 + linux/arm64 images and pushes `latest` and `sha-<hash>`; tagging `v*` additionally pushes version number.

Each model follows its upstream license (ZipVoice: Apache-2.0, MeloTTS: MIT). Repository code is MIT.
