package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/haiboyuwen/sherpa-tts/internal/tts"
)

const maxTextRunes = 600

type gatewayOptions struct {
	registry   *registry
	configFile string
	cacheDir   string
	apiToken   string
	getenv     func(string) string // 测试注入；nil = os.Getenv
}

// gateway 持有提供商服务和配置文件。配置文件存在时以它为准；否则用 TTS_* 环境变量初始化，
// 首次通过 PUT /v1/config 保存后落盘接管。
type gateway struct {
	reg        *registry
	svc        *tts.Service
	configFile string
	apiToken   string
	mu         sync.Mutex // 串行化配置保存
}

func newGateway(opts gatewayOptions) (*gateway, error) {
	g := &gateway{reg: opts.registry, configFile: opts.configFile, apiToken: strings.TrimSpace(opts.apiToken)}
	cfg, ok, loadErr := tts.LoadFile(opts.configFile)
	if loadErr != nil {
		log.Printf("读取 %s 失败，改用环境变量：%v", opts.configFile, loadErr)
	}
	if !ok || loadErr != nil {
		cfg = tts.ConfigFromEnv("TTS_", opts.getenv)
	}
	var builtin []tts.Builtin
	if g.reg != nil && len(g.reg.engines) > 0 {
		builtin = append(builtin, tts.Builtin{ID: "local", Name: "本地模型", Provider: localProvider{reg: g.reg}})
	}
	svc, err := tts.New(cfg, tts.Options{CacheDir: opts.cacheDir, Builtin: builtin, MaxRunes: maxTextRunes})
	g.svc = svc
	return g, err
}

func (g *gateway) routes() http.Handler {
	mux := http.NewServeMux()

	// 兼容旧客户端：只涉及本地引擎。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"engines": g.reg.health()})
	})
	mux.HandleFunc("POST /synthesize", g.handleLegacySynthesize)

	// 统一接口：本地模型 + 第三方。
	mux.Handle("GET /v1/voices", g.auth(g.svc.VoicesHandler()))
	mux.Handle("POST /v1/synthesize", g.auth(g.svc.SynthesizeHandler()))
	mux.Handle("GET /v1/config", g.auth(http.HandlerFunc(g.handleGetConfig)))
	mux.Handle("PUT /v1/config", g.auth(http.HandlerFunc(g.handlePutConfig)))
	mux.Handle("POST /v1/config/test", g.auth(http.HandlerFunc(g.handleTestProvider)))
	return mux
}

// auth：设置了 TTS_API_TOKEN 时，/v1/* 需要 Authorization: Bearer <token>。
// 应用后端持有令牌并自行做用户/管理员鉴权；浏览器不直连本服务。
func (g *gateway) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.apiToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(g.apiToken)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// configResponse 是 GET/PUT /v1/config 的响应：不含密钥明文。
type configResponse struct {
	Config          tts.ConfigView `json:"config"`
	Types           []tts.TypeMeta `json:"types"`
	Voices          []tts.Voice    `json:"voices"`
	Local           localStatus    `json:"local"`
	SettingsStorage string         `json:"settings_storage"`
}

type localStatus struct {
	Enabled bool           `json:"enabled"`
	Engines map[string]any `json:"engines"`
}

func (g *gateway) configResponse(ctx context.Context) configResponse {
	voices := g.svc.Voices(ctx)
	if voices == nil {
		voices = []tts.Voice{}
	}
	return configResponse{
		Config:          g.svc.Config().View(),
		Types:           tts.ProviderTypes(),
		Voices:          voices,
		Local:           localStatus{Enabled: len(g.reg.engines) > 0, Engines: g.reg.health()},
		SettingsStorage: fmt.Sprintf("保存在 sherpa-tts 服务的 %s（0600）；未保存过时使用该服务的 TTS_* 环境变量", g.configFile),
	}
}

func (g *gateway) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, g.configResponse(r.Context()))
}

// handlePutConfig 整体替换第三方提供商配置：同 ID 的密钥留空 = 保留，clear_api_key = 删除。
func (g *gateway) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var next tts.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "配置格式不正确"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	merged := tts.MergeUpdate(g.svc.Config(), next)
	if err := merged.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 先在内存里应用（会额外校验内置 ID 占用），成功再落盘。
	before := g.svc.Config()
	if err := g.svc.Update(merged); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := tts.SaveFile(g.configFile, merged); err != nil {
		_ = g.svc.Update(before)
		log.Printf("保存配置失败：%v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, g.configResponse(r.Context()))
}

type testRequest struct {
	Provider tts.ProviderConfig `json:"provider"`
	Voice    string             `json:"voice"`
	Text     string             `json:"text"`
}

// handleTestProvider 用未保存的草稿配置（密钥留空则沿用已保存的）合成一句试听，成功直接返回音频。
func (g *gateway) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "测试参数格式不正确"})
		return
	}
	provider := tts.ResolveSecret(g.svc.Config(), req.Provider)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	started := time.Now()
	audio, err := tts.TestProvider(ctx, provider, req.Voice, req.Text)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "latency_ms": time.Since(started).Milliseconds()})
		return
	}
	w.Header().Set("Content-Type", audio.ContentType)
	w.Header().Set("X-TTS-Latency-Ms", fmt.Sprint(time.Since(started).Milliseconds()))
	_, _ = w.Write(audio.Data)
}

type legacySynthesizeRequest struct {
	Engine string `json:"engine"`
	Voice  int    `json:"voice"`
	Text   string `json:"text"`
}

func (g *gateway) handleLegacySynthesize(w http.ResponseWriter, req *http.Request) {
	var body legacySynthesizeRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	e, ok := g.reg.engines[body.Engine]
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
		_, _ = w.Write(audio)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
