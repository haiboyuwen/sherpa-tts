package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func sherpaStub(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"engines":{
				"melo":{"ready":true,"voices":[{"id":0,"label":"MeloTTS 中文"}]},
				"kokoro":{"ready":false,"voices":[{"id":3,"label":"Kokoro 中文 3"}]}}}`))
		case "/synthesize":
			if calls != nil {
				calls.Add(1)
			}
			var req struct {
				Engine string `json:"engine"`
				Voice  int    `json:"voice"`
				Text   string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Engine != "melo" || req.Voice != 0 {
				http.Error(w, "bad voice", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("MP3:" + req.Text))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newService(t *testing.T, cfg Config) *Service {
	t.Helper()
	s, err := New(cfg, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceVoicesAggregatesReadyProviders(t *testing.T) {
	sherpa := sherpaStub(t, nil)
	s := newService(t, Config{
		DefaultVoice: "openai:nova",
		Providers: []ProviderConfig{
			{ID: "local", Type: TypeSherpa, BaseURL: sherpa.URL},
			{ID: "openai", Type: TypeOpenAI, APIKey: "k", Voices: []VoiceOption{{ID: "nova", Label: "Nova"}}},
			{ID: "off", Type: TypeOpenAI, Disabled: true},
			{ID: "dead", Type: TypeSherpa, BaseURL: "http://127.0.0.1:1"},
		},
	})
	resp := s.VoicesResponse(context.Background())
	var ids []string
	for _, v := range resp.Voices {
		ids = append(ids, v.ID)
	}
	if got := strings.Join(ids, ","); got != "local:melo:0,openai:nova" {
		t.Fatalf("voices = %s", got)
	}
	if !resp.Available || resp.DefaultVoice != "openai:nova" {
		t.Fatalf("unexpected response %+v", resp)
	}
	if resp.Voices[0].ProviderName != "本地 sherpa-tts" {
		t.Fatalf("provider name = %q", resp.Voices[0].ProviderName)
	}
}

func TestServiceCachesAndRoutesLegacyVoice(t *testing.T) {
	var calls atomic.Int32
	sherpa := sherpaStub(t, &calls)
	s := newService(t, Config{Providers: []ProviderConfig{{ID: "local", Type: TypeSherpa, BaseURL: sherpa.URL}}})
	for i := 0; i < 2; i++ {
		audio, err := s.Synthesize(context.Background(), "local:melo:0", " 你好 ")
		if err != nil || string(audio.Data) != "MP3:你好" || audio.ContentType != "audio/mpeg" {
			t.Fatalf("round %d: %v %q %q", i, err, audio.Data, audio.ContentType)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream called %d times, want 1", calls.Load())
	}
	// 旧客户端存的「melo:0」仍然路由到 sherpa（同一段文字，缓存键不同但结果一致）。
	if _, err := s.Synthesize(context.Background(), "melo:0", "旧音色"); err != nil {
		t.Fatalf("legacy voice: %v", err)
	}
	if _, err := s.Synthesize(context.Background(), "openai:alloy", "你好"); err != ErrUnknownVoice {
		t.Fatalf("removed provider should be unknown voice, got %v", err)
	}
}

func TestServiceRejectsBadInput(t *testing.T) {
	s := newService(t, Config{})
	if _, err := s.Synthesize(context.Background(), "x:y", "你好"); err != ErrUnavailable {
		t.Fatalf("no providers: %v", err)
	}
	sherpa := sherpaStub(t, nil)
	s = newService(t, Config{Providers: []ProviderConfig{{ID: "local", Type: TypeSherpa, BaseURL: sherpa.URL}}})
	if _, err := s.Synthesize(context.Background(), "local:melo:0", strings.Repeat("字", 601)); err != ErrTextTooLong {
		t.Fatalf("too long: %v", err)
	}
	if StatusFor(ErrTextTooLong) != http.StatusRequestEntityTooLarge || StatusFor(ErrUnavailable) != http.StatusServiceUnavailable {
		t.Fatal("status mapping")
	}
}

func TestHandlers(t *testing.T) {
	sherpa := sherpaStub(t, nil)
	s := newService(t, Config{Providers: []ProviderConfig{{ID: "local", Type: TypeSherpa, BaseURL: sherpa.URL}}})
	rec := httptest.NewRecorder()
	s.SynthesizeHandler()(rec, httptest.NewRequest(http.MethodPost, "/tts", strings.NewReader(`{"voice":"local:melo:0","text":"你好"}`)))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Body.String() != "MP3:你好" {
		t.Fatalf("synth: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.SynthesizeHandler()(rec, httptest.NewRequest(http.MethodPost, "/tts", strings.NewReader(`{"voice":"","text":"你好"}`)))
	if rec.Code != 400 {
		t.Fatalf("missing voice status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.VoicesHandler()(rec, httptest.NewRequest(http.MethodGet, "/tts/voices", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("voices: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConfigValidateAndMerge(t *testing.T) {
	bad := []Config{
		{Providers: []ProviderConfig{{ID: "Bad ID", Type: TypeOpenAI}}},
		{Providers: []ProviderConfig{{ID: "a", Type: TypeOpenAI}, {ID: "a", Type: TypeOpenAI}}},
		{Providers: []ProviderConfig{{ID: "a", Type: "nope"}}},
		{Providers: []ProviderConfig{{ID: "a", Type: TypeAzure, APIKey: "k"}}},
		{Providers: []ProviderConfig{{ID: "a", Type: TypeVolcengine, APIKey: "k"}}},
		{Providers: []ProviderConfig{{ID: "a", Type: TypeSherpa, BaseURL: "ftp://tts"}}},
		{Providers: []ProviderConfig{{ID: "a", Type: TypeOpenAI, BaseURL: "https://user:pw@api.example.com"}}},
	}
	for i, c := range bad {
		if err := c.Normalize().Validate(); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	// 停用的提供商不校验必填项。
	if err := (Config{Providers: []ProviderConfig{{ID: "a", Type: TypeAzure, Disabled: true}}}).Validate(); err != nil {
		t.Fatal(err)
	}

	prev := Config{Providers: []ProviderConfig{{ID: "openai", Type: TypeOpenAI, APIKey: "sk-secret-123456"}, {ID: "azure", Type: TypeAzure, APIKey: "az", Region: "eastasia"}}}
	view := prev.View()
	if view.Providers[0].APIKey != "" || !view.Providers[0].HasAPIKey || view.Providers[0].APIKeyHint != "sk-…3456" {
		t.Fatalf("view leaks or hint wrong: %+v", view.Providers[0])
	}
	next := MergeUpdate(prev, Config{Providers: []ProviderConfig{
		{ID: "openai", Type: TypeOpenAI},
		{ID: "azure", Type: TypeAzure, Region: "eastasia", ClearAPIKey: true},
	}})
	if next.Providers[0].APIKey != "sk-secret-123456" || next.Providers[1].APIKey != "" || next.Providers[1].ClearAPIKey {
		t.Fatalf("merge = %+v", next.Providers)
	}
}

func TestConfigFromEnvAndFile(t *testing.T) {
	env := map[string]string{
		"TTS_URL":                     "http://tts:8000/",
		"TTS_OPENAI_API_KEY":          "sk",
		"TTS_OPENAI_VOICES":           "nova=新星, alloy",
		"TTS_VOLCENGINE_APP_ID":       "app",
		"TTS_VOLCENGINE_ACCESS_TOKEN": "tok",
		"TTS_DEFAULT_VOICE":           "openai:nova",
	}
	cfg := ConfigFromEnv("TTS_", func(k string) string { return env[k] })
	if len(cfg.Providers) != 3 || cfg.Providers[0].BaseURL != "http://tts:8000" || cfg.Providers[1].Voices[0] != (VoiceOption{ID: "nova", Label: "新星"}) || cfg.DefaultVoice != "openai:nova" {
		t.Fatalf("env config = %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sub", "tts.json")
	if _, ok, err := LoadFile(path); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	if err := SaveFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", info.Mode().Perm())
	}
	loaded, ok, err := LoadFile(path)
	if err != nil || !ok || len(loaded.Providers) != 3 || loaded.Providers[1].APIKey != "sk" {
		t.Fatalf("loaded = %+v ok=%v err=%v", loaded, ok, err)
	}
}

func TestOpenAIProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer sk" ||
			body["model"] != "gpt-4o-mini-tts" || body["voice"] != "nova" || body["response_format"] != "mp3" || body["instructions"] != "庄重" {
			http.Error(w, "bad", 400)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("OPENAI"))
	}))
	defer srv.Close()
	audio, err := TestProvider(context.Background(), ProviderConfig{Type: TypeOpenAI, BaseURL: srv.URL + "/v1", APIKey: "sk", Instructions: "庄重"}, "nova", "")
	if err != nil || string(audio.Data) != "OPENAI" {
		t.Fatalf("openai: %v %q", err, audio.Data)
	}
}

func TestAzureProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "az" {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/cognitiveservices/voices/list":
			_, _ = w.Write([]byte(`[
				{"ShortName":"en-US-AvaNeural","LocalName":"Ava","Locale":"en-US"},
				{"ShortName":"zh-HK-HiuGaaiNeural","LocalName":"曉佳","Locale":"zh-HK","LocaleName":"中文(香港)"},
				{"ShortName":"zh-CN-XiaoxiaoNeural","LocalName":"晓晓","Locale":"zh-CN"}]`))
		case "/cognitiveservices/v1":
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("X-Microsoft-OutputFormat") == "" || !bytes.Contains(body, []byte(`name="zh-CN-XiaoxiaoNeural"`)) || !bytes.Contains(body, []byte("a &lt; b")) {
				http.Error(w, "bad ssml "+string(body), 400)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("AZURE"))
		}
	}))
	defer srv.Close()
	p, _ := NewProvider(ProviderConfig{Type: TypeAzure, BaseURL: srv.URL, APIKey: "az"}, nil)
	voices, err := p.Voices(context.Background())
	if err != nil || len(voices) != 2 || voices[0].ID != "zh-CN-XiaoxiaoNeural" || voices[1].Label != "曉佳（中文(香港)）" {
		t.Fatalf("azure voices: %v %+v", err, voices)
	}
	audio, err := p.Synthesize(context.Background(), "zh-CN-XiaoxiaoNeural", "a < b")
	if err != nil || string(audio.Data) != "AZURE" {
		t.Fatalf("azure synth: %v %q", err, audio.Data)
	}
}

func TestVolcengineProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			App   struct{ Appid, Token, Cluster string } `json:"app"`
			Audio struct {
				VoiceType string `json:"voice_type"`
				Encoding  string `json:"encoding"`
			} `json:"audio"`
			Request struct{ Text, Operation string } `json:"request"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer;tok" || body.App.Appid != "app" || body.App.Cluster != "volcano_tts" ||
			body.Audio.VoiceType != "BV001_streaming" || body.Audio.Encoding != "mp3" || body.Request.Operation != "query" {
			_, _ = w.Write([]byte(`{"code":3001,"message":"bad request"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":3000,"message":"Success","data":"` + base64.StdEncoding.EncodeToString([]byte("VOLC")) + `"}`))
	}))
	defer srv.Close()
	audio, err := TestProvider(context.Background(), ProviderConfig{Type: TypeVolcengine, BaseURL: srv.URL, AppID: "app", APIKey: "tok"}, "BV001_streaming", "你好")
	if err != nil || string(audio.Data) != "VOLC" {
		t.Fatalf("volc: %v %q", err, audio.Data)
	}
	_, err = TestProvider(context.Background(), ProviderConfig{Type: TypeVolcengine, BaseURL: srv.URL, AppID: "app", APIKey: "wrong"}, "BV001_streaming", "你好")
	if err == nil || !strings.Contains(err.Error(), "3001") {
		t.Fatalf("volc error: %v", err)
	}
}

func TestDashScopeCosyVoice(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-ws/v1/inference/" || r.Header.Get("Authorization") != "bearer ds" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var run struct {
			Header  struct{ Action, TaskID string } `json:"header"`
			Payload struct {
				Model      string `json:"model"`
				Parameters struct {
					Voice  string `json:"voice"`
					Format string `json:"format"`
				} `json:"parameters"`
			} `json:"payload"`
		}
		if conn.ReadJSON(&run) != nil || run.Header.Action != "run-task" || run.Payload.Model != "cosyvoice-v2" || run.Payload.Parameters.Voice != "longxiaochun_v2" || run.Payload.Parameters.Format != "mp3" {
			_ = conn.WriteJSON(map[string]any{"header": map[string]any{"event": "task-failed", "error_code": "Bad", "error_message": "run-task"}})
			return
		}
		_ = conn.WriteJSON(map[string]any{"header": map[string]any{"event": "task-started", "task_id": run.Header.TaskID}})
		var cont struct {
			Header  struct{ Action string }               `json:"header"`
			Payload struct{ Input struct{ Text string } } `json:"payload"`
		}
		if conn.ReadJSON(&cont) != nil || cont.Header.Action != "continue-task" || cont.Payload.Input.Text != "你好" {
			return
		}
		var finish struct{ Header struct{ Action string } }
		if conn.ReadJSON(&finish) != nil || finish.Header.Action != "finish-task" {
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("COSY"))
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("VOICE"))
		_ = conn.WriteJSON(map[string]any{"header": map[string]any{"event": "task-finished"}})
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	audio, err := TestProvider(ctx, ProviderConfig{Type: TypeDashScope, BaseURL: srv.URL, APIKey: "ds"}, "", "你好")
	if err != nil || string(audio.Data) != "COSYVOICE" || audio.ContentType != "audio/mpeg" {
		t.Fatalf("cosyvoice: %v %q", err, audio.Data)
	}
}

func TestDashScopeQwen(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/services/aigc/multimodal-generation/generation":
			var body struct {
				Model string                       `json:"model"`
				Input struct{ Text, Voice string } `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Header.Get("Authorization") != "Bearer ds" || body.Model != "qwen3-tts-flash" || body.Input.Voice != "Cherry" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"code":"InvalidParameter","message":"bad"}`))
				return
			}
			_, _ = w.Write([]byte(`{"output":{"audio":{"url":"` + srv.URL + `/audio.wav"}}}`))
		case "/audio.wav":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("RIFFWAV"))
		}
	}))
	defer srv.Close()
	audio, err := TestProvider(context.Background(), ProviderConfig{Type: TypeDashScope, BaseURL: srv.URL, APIKey: "ds", Model: "qwen3-tts-flash"}, "", "你好")
	if err != nil || string(audio.Data) != "RIFFWAV" || audio.ContentType != "audio/wav" {
		t.Fatalf("qwen: %v %q %q", err, audio.Data, audio.ContentType)
	}
}

func TestPruneEvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(Config{}, Options{CacheDir: dir, CacheLimit: 100})
	old := filepath.Join(dir, "aa", "old.mp3")
	fresh := filepath.Join(dir, "bb", "fresh.wav")
	for _, p := range []string{old, fresh} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, make([]byte, 60), 0o644)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(old, past, past)
	s.PruneCache()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("oldest file should be evicted")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("newest file should survive")
	}
}
