// Command sherpa-tts 是本地中文神经网络 TTS 服务：sherpa-onnx + Kokoro / MeloTTS，纯 CPU 推理，输出 mp3。
//
//	POST /synthesize  {"engine": "kokoro"|"melo", "voice": 3, "text": "..."}  -> audio/mpeg
//	GET  /health      -> {"engines": {"kokoro": {"label", "ready", "error", "voices": [...]}, ...}}
//
// 模型首次启动时从 sherpa-onnx 的 GitHub release 下载到 TTS_MODEL_DIR（挂卷即可持久化）。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxTextRunes = 600

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "探测本机 /health 后退出（供 Docker HEALTHCHECK 使用）")
	flag.Parse()

	port := env("TTS_PORT", "8000")
	if *healthcheck {
		os.Exit(probe(port))
	}

	threads, _ := strconv.Atoi(env("TTS_THREADS", "4"))
	if threads <= 0 {
		threads = 4
	}
	reg := newRegistry(
		env("TTS_MODEL_DIR", "/models"),
		env("TTS_MODEL_MIRROR", "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models"),
		threads,
		strings.Split(env("TTS_ENGINES", "melo,kokoro"), ","),
	)
	reg.loadAll()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"engines": reg.health()})
	})
	mux.HandleFunc("POST /synthesize", reg.handleSynthesize)

	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("sherpa-tts listening on :%s, engines=%v", port, reg.names())
	log.Fatal(srv.ListenAndServe())
}

func probe(port string) int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

type synthesizeRequest struct {
	Engine string `json:"engine"`
	Voice  int    `json:"voice"`
	Text   string `json:"text"`
}

func (r *registry) handleSynthesize(w http.ResponseWriter, req *http.Request) {
	var body synthesizeRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	e, ok := r.engines[body.Engine]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown engine"})
		return
	}
	if utf8.RuneCountInString(body.Text) > maxTextRunes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "text too long"})
		return
	}
	audio, err := e.synthesize(body.Voice, body.Text)
	switch {
	case errors.Is(err, errNotReady):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not ready"})
	case errors.Is(err, errEmptyText):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty text"})
	case err != nil:
		log.Printf("[%s] synth failed: %v", body.Engine, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "synthesis failed"})
	default:
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		_, _ = w.Write(audio)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
