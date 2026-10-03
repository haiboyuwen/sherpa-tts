package tts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"
)

// Options 是 [Service] 的运行参数，零值都有合理默认。
type Options struct {
	CacheDir    string    // 合成结果磁盘缓存目录；空 = 不缓存
	Builtin     []Builtin // 内置提供商（本地模型），排在配置的第三方之前，ID 不能被配置占用
	CacheLimit  int64     // 缓存容量上限（字节），默认 512MB，超出按最近使用淘汰到 80%
	MaxInFlight int       // 同时向上游发起的合成数，默认 4；公开接口靠它防止把 CPU / 配额打满
	MaxRunes    int       // 单段文字上限，默认 600
	HTTPClient  *http.Client
}

// Voice 是对外暴露的音色，ID 为「提供商ID:音色ID」。
type Voice struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	// ProviderName 是提供商显示名，前端可据此分组。
	ProviderName string `json:"provider_name"`
}

// Builtin 是不经配置、由程序直接提供的提供商（sherpa-tts 的本地模型）。
type Builtin struct {
	ID       string
	Name     string
	Provider Provider
}

type entry struct {
	cfg      ProviderConfig
	provider Provider
	builtin  bool
}

// Service 汇总多家提供商，负责音色列表、按音色路由合成、磁盘缓存和并发控制。可并发使用，配置可热更新。
type Service struct {
	opts  Options
	slots chan struct{}
	group singleflight.Group

	mu      sync.RWMutex
	cfg     Config
	entries []entry

	writes atomic.Int64
	prune  sync.Mutex
}

// ErrUnavailable 表示没有任何可用的提供商。
var ErrUnavailable = errors.New("未配置可用的语音合成服务")

// ErrUnknownVoice 表示音色 ID 找不到对应的提供商。
var ErrUnknownVoice = errors.New("未知音色")

// ErrTextTooLong 表示单段文字超过上限。
var ErrTextTooLong = errors.New("单段文字过长")

// New 创建服务。cfg 无效时返回错误（但仍返回可用的空服务，方便应用先启动再到后台修正）。
func New(cfg Config, opts Options) (*Service, error) {
	if opts.CacheLimit <= 0 {
		opts.CacheLimit = 512 << 20
	}
	if opts.MaxInFlight <= 0 {
		opts.MaxInFlight = 4
	}
	if opts.MaxRunes <= 0 {
		opts.MaxRunes = 600
	}
	s := &Service{opts: opts, slots: make(chan struct{}, opts.MaxInFlight)}
	return s, s.Update(cfg)
}

// Update 替换配置。校验失败时保留旧配置并返回错误。
func (s *Service) Update(cfg Config) error {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	entries := make([]entry, 0, len(s.opts.Builtin)+len(cfg.Providers))
	for _, b := range s.opts.Builtin {
		entries = append(entries, entry{cfg: ProviderConfig{ID: b.ID, Type: "builtin", Name: b.Name}, provider: b.Provider, builtin: true})
	}
	for _, pc := range cfg.Providers {
		if s.reserved(pc.ID) {
			return fmt.Errorf("提供商 ID %q 已被内置提供商占用", pc.ID)
		}
		if pc.Disabled {
			continue
		}
		hc := s.opts.HTTPClient
		if hc == nil {
			hc = &http.Client{Timeout: time.Duration(pc.timeoutOr(60)) * time.Second}
		}
		p, err := NewProvider(pc, hc)
		if err != nil {
			return err
		}
		entries = append(entries, entry{cfg: pc, provider: p})
	}
	s.mu.Lock()
	s.cfg, s.entries = cfg, entries
	s.mu.Unlock()
	return nil
}

func (s *Service) reserved(id string) bool {
	for _, b := range s.opts.Builtin {
		if b.ID == id {
			return true
		}
	}
	return false
}

// Config 返回当前配置（含密钥，勿直接返回给前端，用 View）。
func (s *Service) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Enabled 报告是否至少有一个启用的提供商。
func (s *Service) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries) > 0
}

func (s *Service) snapshot() ([]entry, Config) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries, s.cfg
}

// Voices 并发查询所有启用的提供商，按配置顺序返回可用音色；单个提供商失败只是没有它的音色。
func (s *Service) Voices(ctx context.Context) []Voice {
	entries, _ := s.snapshot()
	results := make([][]Voice, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e entry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			list, err := e.provider.Voices(ctx)
			if err != nil {
				return
			}
			name := e.cfg.DisplayName()
			for _, v := range list {
				label := v.Label
				if label == "" {
					label = v.ID
				}
				results[i] = append(results[i], Voice{ID: e.cfg.ID + ":" + v.ID, Label: label, Provider: e.cfg.ID, ProviderName: name})
			}
		}(i, e)
	}
	wg.Wait()
	var out []Voice
	for _, list := range results {
		out = append(out, list...)
	}
	return out
}

// route 把全局音色 ID 拆成提供商和其内部音色。兼容旧版只有「引擎:编号」的本地音色（如 melo:0）。
func (s *Service) route(voiceID string) (entry, string, bool) {
	entries, _ := s.snapshot()
	providerID, local, ok := strings.Cut(voiceID, ":")
	if ok && local != "" {
		for _, e := range entries {
			if e.cfg.ID == providerID {
				return e, local, true
			}
		}
	}
	// 旧客户端存的是本地模型的「引擎:编号」（如 melo:0），交给第一个内置提供商。
	if engine, sid, ok := strings.Cut(voiceID, ":"); ok && engine != "" && isDigits(sid) {
		for _, e := range entries {
			if e.builtin {
				return e, voiceID, true
			}
		}
	}
	return entry{}, "", false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Synthesize 合成一段文字，命中缓存直接返回。同一段并发请求只合成一次。
func (s *Service) Synthesize(ctx context.Context, voiceID, text string) (Audio, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Audio{}, errEmptyText
	}
	if utf8.RuneCountInString(text) > s.opts.MaxRunes {
		return Audio{}, ErrTextTooLong
	}
	if !s.Enabled() {
		return Audio{}, ErrUnavailable
	}
	e, local, ok := s.route(strings.TrimSpace(voiceID))
	if !ok {
		return Audio{}, ErrUnknownVoice
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{e.cfg.Type, e.cfg.ID, e.cfg.BaseURL, e.cfg.Model, e.cfg.Instructions, local, text}, "\x00")))
	key := hex.EncodeToString(sum[:])
	if audio, ok := s.readCache(key); ok {
		return audio, nil
	}
	v, err, _ := s.group.Do(key, func() (any, error) {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		audio, err := e.provider.Synthesize(ctx, local, text)
		if err != nil {
			return nil, err
		}
		s.writeCache(key, audio)
		return audio, nil
	})
	if err != nil {
		return Audio{}, err
	}
	return v.(Audio), nil
}

var cacheExts = map[string]string{"audio/mpeg": ".mp3", "audio/mp3": ".mp3", "audio/wav": ".wav", "audio/x-wav": ".wav", "audio/wave": ".wav", "audio/ogg": ".ogg", "audio/opus": ".opus", "audio/aac": ".aac"}
var extTypes = map[string]string{".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".aac": "audio/aac"}

func (s *Service) cachePath(key, ext string) string {
	return filepath.Join(s.opts.CacheDir, key[:2], key+ext)
}

func (s *Service) readCache(key string) (Audio, bool) {
	if s.opts.CacheDir == "" {
		return Audio{}, false
	}
	for ext, ct := range extTypes {
		path := s.cachePath(key, ext)
		if data, err := os.ReadFile(path); err == nil {
			now := time.Now()
			_ = os.Chtimes(path, now, now) // 命中即续命，淘汰按最近使用
			return Audio{Data: data, ContentType: ct}, true
		}
	}
	return Audio{}, false
}

func (s *Service) writeCache(key string, audio Audio) {
	if s.opts.CacheDir == "" {
		return
	}
	ext, ok := cacheExts[strings.ToLower(strings.TrimSpace(strings.Split(audio.ContentType, ";")[0]))]
	if !ok {
		return
	}
	path := s.cachePath(key, ext)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, audio.Data, 0o644) == nil && os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
	if s.writes.Add(1)%64 == 0 {
		go s.PruneCache()
	}
}

// PruneCache 容量超限时删最久没用的文件，直到降到上限的 80%。
func (s *Service) PruneCache() {
	if s.opts.CacheDir == "" || !s.prune.TryLock() {
		return
	}
	defer s.prune.Unlock()
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	_ = filepath.WalkDir(s.opts.CacheDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || extTypes[filepath.Ext(p)] == "" {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files = append(files, file{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= s.opts.CacheLimit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= s.opts.CacheLimit*8/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// TestProvider 用给定配置合成一小段文字，供后台「测试」按钮使用（不经过缓存）。voice 为空时取第一个音色。
func TestProvider(ctx context.Context, cfg ProviderConfig, voice, text string) (Audio, error) {
	cfg = Config{Providers: []ProviderConfig{cfg}}.Normalize().Providers[0]
	cfg.Disabled = false
	if cfg.ID == "" {
		cfg.ID = "test"
	}
	if err := (Config{Providers: []ProviderConfig{cfg}}).Validate(); err != nil {
		return Audio{}, err
	}
	p, err := NewProvider(cfg, nil)
	if err != nil {
		return Audio{}, err
	}
	if voice == "" {
		voices, err := p.Voices(ctx)
		if err != nil {
			return Audio{}, err
		}
		if len(voices) == 0 {
			return Audio{}, &ProviderError{Provider: cfg.DisplayName(), Message: "没有可用音色"}
		}
		voice = voices[0].ID
	}
	if strings.TrimSpace(text) == "" {
		text = "濡须宇文氏宗谱，语音朗读测试。"
	}
	return p.Synthesize(ctx, voice, text)
}

// ===== HTTP =====

// VoicesResponse 是 VoicesHandler 的响应体。
type VoicesResponse struct {
	Available    bool    `json:"available"`
	Voices       []Voice `json:"voices"`
	DefaultVoice string  `json:"default_voice,omitempty"`
}

// VoicesResponse 汇总音色并解析默认音色（配置的默认音色不在列表里时忽略）。
func (s *Service) VoicesResponse(ctx context.Context) VoicesResponse {
	voices := s.Voices(ctx)
	if voices == nil {
		voices = []Voice{}
	}
	_, cfg := s.snapshot()
	resp := VoicesResponse{Available: len(voices) > 0, Voices: voices}
	for _, v := range voices {
		if v.ID == cfg.DefaultVoice {
			resp.DefaultVoice = v.ID
			break
		}
	}
	return resp
}

// VoicesHandler 处理 GET：返回 {available, voices, default_voice}。
func (s *Service) VoicesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, max-age=60")
		writeJSON(w, http.StatusOK, s.VoicesResponse(r.Context()))
	}
}

// SynthesizeRequest 是 SynthesizeHandler 的请求体。
type SynthesizeRequest struct {
	Voice string `json:"voice"`
	Text  string `json:"text"`
}

// SynthesizeHandler 处理 POST {voice, text}：返回音频。语速由前端 playbackRate 调，不进缓存键。
func (s *Service) SynthesizeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SynthesizeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数错误"})
			return
		}
		if strings.TrimSpace(req.Voice) == "" || strings.TrimSpace(req.Text) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要音色和文字"})
			return
		}
		audio, err := s.Synthesize(r.Context(), req.Voice, req.Text)
		if err != nil {
			writeJSON(w, StatusFor(err), map[string]string{"error": PublicMessage(err)})
			return
		}
		w.Header().Set("Content-Type", audio.ContentType)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio.Data)
	}
}

// StatusFor 把错误映射成 HTTP 状态码。
func StatusFor(err error) int {
	switch {
	case errors.Is(err, errEmptyText), errors.Is(err, ErrUnknownVoice):
		return http.StatusBadRequest
	case errors.Is(err, ErrTextTooLong):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// PublicMessage 返回可以给公开访客看的错误说明（不泄露上游细节）。
func PublicMessage(err error) string {
	switch {
	case errors.Is(err, errEmptyText):
		return "需要音色和文字"
	case errors.Is(err, ErrUnknownVoice):
		return "音色不可用，请重新选择"
	case errors.Is(err, ErrTextTooLong):
		return ErrTextTooLong.Error()
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable.Error()
	}
	return "朗读合成失败"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
