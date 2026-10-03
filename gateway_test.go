package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 网关层只测接线：鉴权、配置读写与脱敏、落盘、试听。提供商协议见 internal/tts。

func newTestGateway(t *testing.T, token string, env map[string]string) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	g, err := newGateway(gatewayOptions{
		registry:   newRegistry(dir, "", 1, nil), // 无本地引擎
		configFile: file,
		cacheDir:   filepath.Join(dir, "cache"),
		apiToken:   token,
		getenv:     func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	return g.routes(), file
}

func call(h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGatewayTokenGuardsV1Only(t *testing.T) {
	h, _ := newTestGateway(t, "secret-token", nil)
	if rec := call(h, http.MethodGet, "/v1/voices", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/config", "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/voices", "secret-token", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("with token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/health", "", nil); rec.Code != 200 {
		t.Fatalf("health must stay open for Docker healthcheck: %d", rec.Code)
	}
}

func TestGatewayConfigLifecycle(t *testing.T) {
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-saved-key-0001" {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("OPENAI-MP3"))
	}))
	defer openai.Close()

	// 未保存过配置时用环境变量初始化。
	h, file := newTestGateway(t, "", map[string]string{"TTS_AZURE_API_KEY": "az-env", "TTS_AZURE_REGION": "eastasia"})
	rec := call(h, http.MethodGet, "/v1/config", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"azure"`) || strings.Contains(rec.Body.String(), "az-env") {
		t.Fatalf("env config: %d %s", rec.Code, rec.Body.String())
	}

	update := map[string]any{"providers": []map[string]any{
		{"id": "openai", "type": "openai", "base_url": openai.URL, "api_key": "sk-saved-key-0001", "voices": []map[string]string{{"id": "nova"}}},
	}, "default_voice": "openai:nova"}
	rec = call(h, http.MethodPut, "/v1/config", "", update)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "sk-saved-key-0001") || !strings.Contains(rec.Body.String(), `"has_api_key":true`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v", err)
	}

	// 合成走新配置；音色列表与默认音色热更新。
	if rec = call(h, http.MethodPost, "/v1/synthesize", "", map[string]string{"voice": "openai:nova", "text": "你好"}); rec.Code != 200 || rec.Body.String() != "OPENAI-MP3" {
		t.Fatalf("synth: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(h, http.MethodGet, "/v1/voices", "", nil); !strings.Contains(rec.Body.String(), `"default_voice":"openai:nova"`) {
		t.Fatalf("voices: %s", rec.Body.String())
	}

	// 试听：密钥留空沿用已保存的。
	test := map[string]any{"provider": map[string]any{"id": "openai", "type": "openai", "base_url": openai.URL}}
	if rec = call(h, http.MethodPost, "/v1/config/test", "", test); rec.Code != 200 || rec.Body.String() != "OPENAI-MP3" {
		t.Fatalf("test: %d %s", rec.Code, rec.Body.String())
	}
	// 留空密钥 = 保留；无效配置 400 且不落盘。
	update["providers"].([]map[string]any)[0]["api_key"] = ""
	if rec = call(h, http.MethodPut, "/v1/config", "", update); rec.Code != 200 {
		t.Fatalf("second put: %d", rec.Code)
	}
	if rec = call(h, http.MethodPut, "/v1/config", "", map[string]any{"providers": []map[string]any{{"id": "x", "type": "nope"}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid put: %d", rec.Code)
	}
	saved, _ := os.ReadFile(file)
	if !strings.Contains(string(saved), "sk-saved-key-0001") || strings.Contains(string(saved), `"nope"`) {
		t.Fatalf("saved config: %s", saved)
	}
}
