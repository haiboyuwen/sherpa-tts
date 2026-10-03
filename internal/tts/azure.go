package tts

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// azureProvider 调用 Azure AI Speech 的 REST 合成接口（SSML）。
type azureProvider struct {
	cfg ProviderConfig
	hc  *http.Client

	mu       sync.Mutex
	listed   []VoiceOption
	listedAt time.Time
}

func (p *azureProvider) endpoint() string {
	if p.cfg.BaseURL != "" {
		return p.cfg.BaseURL
	}
	return "https://" + p.cfg.Region + ".tts.speech.microsoft.com"
}

// Voices 没配音色时在线列出全部中文音色（缓存 1 小时）；查询失败退回默认音色。
func (p *azureProvider) Voices(ctx context.Context) ([]VoiceOption, error) {
	if len(p.cfg.Voices) > 0 {
		return p.cfg.Voices, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listed != nil && time.Since(p.listedAt) < time.Hour {
		return p.listed, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint()+"/cognitiveservices/voices/list", nil)
	if err != nil {
		return defaultVoices(TypeAzure), nil
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", p.cfg.APIKey)
	resp, err := p.hc.Do(req)
	if err != nil {
		return defaultVoices(TypeAzure), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, upstreamError("Azure", resp)
	}
	var list []struct {
		ShortName  string `json:"ShortName"`
		LocalName  string `json:"LocalName"`
		Locale     string `json:"Locale"`
		LocaleName string `json:"LocaleName"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return defaultVoices(TypeAzure), nil
	}
	var voices []VoiceOption
	for _, v := range list {
		if !strings.HasPrefix(strings.ToLower(v.Locale), "zh-") {
			continue
		}
		label := v.LocalName
		if v.Locale != "zh-CN" && v.LocaleName != "" {
			label += "（" + v.LocaleName + "）"
		}
		voices = append(voices, VoiceOption{ID: v.ShortName, Label: label})
	}
	// zh-CN 在前，其余（粤语、台湾等）在后。
	sort.SliceStable(voices, func(i, j int) bool {
		return strings.HasPrefix(voices[i].ID, "zh-CN-") && !strings.HasPrefix(voices[j].ID, "zh-CN-")
	})
	if len(voices) == 0 {
		voices = defaultVoices(TypeAzure)
	}
	p.listed, p.listedAt = voices, time.Now()
	return voices, nil
}

func (p *azureProvider) Synthesize(ctx context.Context, voice, text string) (Audio, error) {
	locale := "zh-CN"
	if parts := strings.SplitN(voice, "-", 3); len(parts) == 3 {
		locale = parts[0] + "-" + parts[1]
	}
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(text))
	var voiceAttr strings.Builder
	_ = xml.EscapeText(&voiceAttr, []byte(voice))
	ssml := `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="` + locale + `">` +
		`<voice name="` + voiceAttr.String() + `">` + escaped.String() + `</voice></speak>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint()+"/cognitiveservices/v1", strings.NewReader(ssml))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", "audio-24khz-48kbitrate-mono-mp3")
	req.Header.Set("User-Agent", "sherpa-tts-go")
	resp, err := p.hc.Do(req)
	if err != nil {
		return Audio{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("Azure", resp)
	}
	return readAudio("Azure", resp, "audio/mpeg")
}
