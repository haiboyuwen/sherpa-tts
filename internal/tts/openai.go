package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// openAIProvider 调用 POST {base}/audio/speech。
type openAIProvider struct {
	cfg ProviderConfig
	hc  *http.Client
}

func (p *openAIProvider) Voices(context.Context) ([]VoiceOption, error) {
	return configuredVoices(p.cfg), nil
}

func (p *openAIProvider) Synthesize(ctx context.Context, voice, text string) (Audio, error) {
	base := p.cfg.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := p.cfg.Model
	if model == "" {
		model = "gpt-4o-mini-tts"
	}
	payload := map[string]any{"model": model, "input": text, "voice": voice, "response_format": "mp3"}
	if p.cfg.Instructions != "" {
		payload["instructions"] = p.cfg.Instructions
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return Audio{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("OpenAI", resp)
	}
	return readAudio("OpenAI", resp, "audio/mpeg")
}
