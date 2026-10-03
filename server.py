"""sherpa-tts：本地中文神经网络 TTS 服务（sherpa-onnx + Kokoro / MeloTTS，纯 CPU 推理）。

作为 sidecar 部署在应用旁边，由应用后端代理调用（不直接暴露给浏览器）。

POST /synthesize  {"engine": "kokoro"|"melo", "voice": 3, "text": "..."}  -> audio/mpeg
GET  /health      -> {"engines": {"kokoro": {"ready": true, "voices": [...]}, ...}}

模型首次启动时从 sherpa-onnx 的 GitHub release 下载到 TTS_MODEL_DIR（挂卷即可持久化）。
"""
import io
import json
import os
import re
import tarfile
import threading
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import lameenc
import numpy as np
import sherpa_onnx as so

MODEL_DIR = os.environ.get("TTS_MODEL_DIR", "/models")
PORT = int(os.environ.get("TTS_PORT", "8000"))
THREADS = int(os.environ.get("TTS_THREADS", "4"))
MIRROR = os.environ.get("TTS_MODEL_MIRROR", "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models")
MAX_TEXT = 600

ENGINES = {
    "kokoro": {
        "archive": "kokoro-multi-lang-v1_1",
        "label": "Kokoro",
        "voices": [{"id": 3, "label": "Kokoro 中文 3"}],
    },
    "melo": {
        "archive": "vits-melo-tts-zh_en",
        "label": "MeloTTS",
        "voices": [{"id": 0, "label": "MeloTTS 中文"}],
    },
}
enabled = [e.strip() for e in os.environ.get("TTS_ENGINES", "melo,kokoro").split(",") if e.strip() in ENGINES]

state = {name: {"ready": False, "error": ""} for name in enabled}
engines = {}
locks = {name: threading.Lock() for name in enabled}


def log(msg):
    print(msg, flush=True)


def download(name):
    d = os.path.join(MODEL_DIR, ENGINES[name]["archive"])
    if os.path.isdir(d) and os.path.exists(os.path.join(d, ".complete")):
        return d
    os.makedirs(MODEL_DIR, exist_ok=True)
    url = f"{MIRROR}/{ENGINES[name]['archive']}.tar.bz2"
    log(f"[{name}] downloading {url}")
    with urllib.request.urlopen(url, timeout=60) as resp, tarfile.open(fileobj=resp, mode="r|bz2") as tar:
        tar.extractall(MODEL_DIR, filter="data")
    open(os.path.join(d, ".complete"), "w").close()
    return d


def build(name, d):
    if name == "kokoro":
        cfg = so.OfflineTtsConfig(
            model=so.OfflineTtsModelConfig(
                kokoro=so.OfflineTtsKokoroModelConfig(
                    model=f"{d}/model.onnx",
                    voices=f"{d}/voices.bin",
                    tokens=f"{d}/tokens.txt",
                    data_dir=f"{d}/espeak-ng-data",
                    dict_dir=f"{d}/dict",
                    lexicon=f"{d}/lexicon-zh.txt,{d}/lexicon-us-en.txt",
                    lang="zh",
                ),
                num_threads=THREADS,
                provider="cpu",
            ),
            rule_fsts=f"{d}/date-zh.fst,{d}/number-zh.fst,{d}/phone-zh.fst",
        )
    else:
        cfg = so.OfflineTtsConfig(
            model=so.OfflineTtsModelConfig(
                vits=so.OfflineTtsVitsModelConfig(
                    model=f"{d}/model.onnx",
                    lexicon=f"{d}/lexicon.txt",
                    tokens=f"{d}/tokens.txt",
                    dict_dir=f"{d}/dict",
                ),
                num_threads=THREADS,
                provider="cpu",
            ),
            rule_fsts=f"{d}/date.fst,{d}/number.fst,{d}/phone.fst",
        )
    return so.OfflineTts(cfg)


def load(name):
    try:
        engines[name] = build(name, download(name))
        state[name]["ready"] = True
        log(f"[{name}] ready")
    except Exception as exc:  # noqa: BLE001 - 单个引擎失败不影响另一个
        state[name]["error"] = str(exc)
        log(f"[{name}] failed: {exc}")


# 模型的词表不认弯引号、书名号等，统一压成普通标点。
_PUNCT = str.maketrans({
    "“": "，", "”": "，", "‘": "，", "’": "，", "「": "，", "」": "，", "『": "，", "』": "，",
    "《": "", "》": "", "（": "，", "）": "，", "(": "，", ")": "，", "—": "，", "…": "。",
})


def normalize(text):
    text = text.translate(_PUNCT)
    text = re.sub(r"[，,]{2,}", "，", text)
    return re.sub(r"\s+", " ", text).strip()


def encode_mp3(samples, rate):
    pcm = (np.clip(np.asarray(samples, dtype=np.float32), -1, 1) * 32767).astype(np.int16)
    enc = lameenc.Encoder()
    enc.set_bit_rate(64)
    enc.set_in_sample_rate(rate)
    enc.set_channels(1)
    enc.set_quality(2)
    return bytes(enc.encode(pcm.tobytes()) + enc.flush())


def synthesize(engine, voice, text):
    text = normalize(text)
    if not text:
        raise ValueError("empty text")
    with locks[engine]:
        audio = engines[engine].generate(text, sid=voice, speed=1.0)
    return encode_mp3(audio.samples, audio.sample_rate)


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, body, ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _json(self, code, obj):
        self._send(code, json.dumps(obj, ensure_ascii=False).encode())

    def do_GET(self):
        if self.path != "/health":
            return self._json(404, {"error": "not found"})
        out = {}
        for name in enabled:
            out[name] = {
                "label": ENGINES[name]["label"],
                "ready": state[name]["ready"],
                "error": state[name]["error"],
                "voices": ENGINES[name]["voices"],
            }
        self._json(200, {"engines": out})

    def do_POST(self):
        if self.path != "/synthesize":
            return self._json(404, {"error": "not found"})
        try:
            n = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(n))
            engine, voice, text = req.get("engine"), int(req.get("voice", 0)), str(req.get("text", ""))
        except (ValueError, TypeError):
            return self._json(400, {"error": "bad request"})
        if engine not in state:
            return self._json(400, {"error": "unknown engine"})
        if not state[engine]["ready"]:
            return self._json(503, {"error": "engine not ready"})
        if len(text) > MAX_TEXT:
            return self._json(413, {"error": "text too long"})
        try:
            self._send(200, synthesize(engine, voice, text), "audio/mpeg")
        except ValueError as exc:
            self._json(400, {"error": str(exc)})
        except Exception as exc:  # noqa: BLE001
            log(f"synth failed: {exc}")
            self._json(500, {"error": "synthesis failed"})

    def log_message(self, fmt, *args):  # 静音逐请求日志
        pass


if __name__ == "__main__":
    for name in enabled:
        threading.Thread(target=load, args=(name,), daemon=True).start()
    log(f"tts sidecar listening on :{PORT}, engines={enabled}")
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
