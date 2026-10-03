package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sherpaProvider 调用本地 sherpa-tts 服务。音色 ID 为「引擎:speaker」，如 melo:0。
type sherpaProvider struct {
	cfg ProviderConfig
	hc  *http.Client
}

func (p *sherpaProvider) Voices(ctx context.Context) ([]VoiceOption, error) {
	if len(p.cfg.Voices) > 0 {
		return p.cfg.Voices, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, upstreamError("sherpa-tts", resp)
	}
	var health struct {
		Engines map[string]struct {
			Ready  bool `json:"ready"`
			Voices []struct {
				ID    int    `json:"id"`
				Label string `json:"label"`
			} `json:"voices"`
		} `json:"engines"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(health.Engines))
	for name := range health.Engines {
		names = append(names, name)
	}
	sort.Strings(names)
	var voices []VoiceOption
	for _, name := range names {
		engine := health.Engines[name]
		if !engine.Ready {
			continue
		}
		for _, v := range engine.Voices {
			voices = append(voices, VoiceOption{ID: name + ":" + strconv.Itoa(v.ID), Label: v.Label})
		}
	}
	return voices, nil
}

func (p *sherpaProvider) Synthesize(ctx context.Context, voice, text string) (Audio, error) {
	engine, sid, ok := strings.Cut(voice, ":")
	id, err := strconv.Atoi(sid)
	if !ok || engine == "" || err != nil {
		return Audio{}, &ProviderError{Provider: "sherpa-tts", Message: "音色应为「引擎:编号」，如 melo:0"}
	}
	body, _ := json.Marshal(map[string]any{"engine": engine, "voice": id, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/synthesize", bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return Audio{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("sherpa-tts", resp)
	}
	return readAudio("sherpa-tts", resp, "audio/mpeg")
}
