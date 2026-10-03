package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// volcengineProvider 调用火山引擎豆包语音合成 HTTP v1（非流式）：POST /api/v1/tts。
type volcengineProvider struct {
	cfg ProviderConfig
	hc  *http.Client
}

func (p *volcengineProvider) Voices(context.Context) ([]VoiceOption, error) {
	return configuredVoices(p.cfg), nil
}

func (p *volcengineProvider) Synthesize(ctx context.Context, voice, text string) (Audio, error) {
	base := p.cfg.BaseURL
	if base == "" {
		base = "https://openspeech.bytedance.com"
	}
	cluster := p.cfg.Cluster
	if cluster == "" {
		cluster = "volcano_tts"
	}
	body, _ := json.Marshal(map[string]any{
		"app":   map[string]any{"appid": p.cfg.AppID, "token": p.cfg.APIKey, "cluster": cluster},
		"user":  map[string]any{"uid": "sherpa-tts"},
		"audio": map[string]any{"voice_type": voice, "encoding": "mp3", "speed_ratio": 1.0},
		"request": map[string]any{
			"reqid": newRequestID(), "text": text, "text_type": "plain", "operation": "query",
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/tts", bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// 火山引擎的鉴权头是「Bearer;token」（分号，不是空格）。
	req.Header.Set("Authorization", "Bearer;"+p.cfg.APIKey)
	resp, err := p.hc.Do(req)
	if err != nil {
		return Audio{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes*2))
	if err != nil {
		return Audio{}, err
	}
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Audio{}, &ProviderError{Provider: "火山引擎", Status: resp.StatusCode, Message: "无法解析响应"}
	}
	if out.Code != 3000 || out.Data == "" {
		return Audio{}, &ProviderError{Provider: "火山引擎", Status: resp.StatusCode, Message: strconv.Itoa(out.Code) + " " + out.Message}
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return Audio{}, &ProviderError{Provider: "火山引擎", Message: "音频解码失败"}
	}
	return Audio{Data: data, ContentType: "audio/mpeg"}, nil
}
