package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// dashScopeProvider 调用阿里云百炼语音合成：
//   - cosyvoice-* 走 WebSocket 双工协议（run-task / continue-task / finish-task），输出 mp3；
//   - qwen-tts、qwen3-tts-* 走 HTTP 多模态生成接口，返回 wav 下载地址。
type dashScopeProvider struct {
	cfg ProviderConfig
	hc  *http.Client
}

var qwenDefaultVoices = []VoiceOption{
	{ID: "Cherry", Label: "Cherry 芊悦"}, {ID: "Ethan", Label: "Ethan 晨煦"},
	{ID: "Chelsie", Label: "Chelsie"}, {ID: "Serena", Label: "Serena"},
}

func (p *dashScopeProvider) model() string {
	if p.cfg.Model != "" {
		return p.cfg.Model
	}
	return "cosyvoice-v2"
}

func (p *dashScopeProvider) isQwen() bool {
	return strings.HasPrefix(strings.ToLower(p.model()), "qwen")
}

func (p *dashScopeProvider) base() string {
	if p.cfg.BaseURL != "" {
		return p.cfg.BaseURL
	}
	return "https://dashscope.aliyuncs.com"
}

func (p *dashScopeProvider) Voices(context.Context) ([]VoiceOption, error) {
	if len(p.cfg.Voices) == 0 && p.isQwen() {
		return qwenDefaultVoices, nil
	}
	return configuredVoices(p.cfg), nil
}

func (p *dashScopeProvider) Synthesize(ctx context.Context, voice, text string) (Audio, error) {
	if p.isQwen() {
		return p.synthesizeQwen(ctx, voice, text)
	}
	return p.synthesizeCosyVoice(ctx, voice, text)
}

func (p *dashScopeProvider) synthesizeQwen(ctx context.Context, voice, text string) (Audio, error) {
	body, _ := json.Marshal(map[string]any{
		"model": p.model(),
		"input": map[string]any{"text": text, "voice": voice, "language_type": "Chinese"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/api/v1/services/aigc/multimodal-generation/generation", bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	resp, err := p.hc.Do(req)
	if err != nil {
		return Audio{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("阿里云百炼", resp)
	}
	var out struct {
		Output struct {
			Audio struct {
				URL string `json:"url"`
			} `json:"audio"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Audio{}, err
	}
	if out.Output.Audio.URL == "" {
		return Audio{}, &ProviderError{Provider: "阿里云百炼", Message: strings.TrimSpace(out.Code + " " + out.Message + " 未返回音频地址")}
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, out.Output.Audio.URL, nil)
	if err != nil {
		return Audio{}, err
	}
	audioResp, err := p.hc.Do(get)
	if err != nil {
		return Audio{}, err
	}
	defer audioResp.Body.Close()
	if audioResp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("阿里云百炼", audioResp)
	}
	return readAudio("阿里云百炼", audioResp, "audio/wav")
}

type dashScopeEvent struct {
	Header struct {
		Event        string `json:"event"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
}

func (p *dashScopeProvider) synthesizeCosyVoice(ctx context.Context, voice, text string) (Audio, error) {
	wsURL := strings.Replace(strings.Replace(p.base(), "https://", "wss://", 1), "http://", "ws://", 1) + "/api-ws/v1/inference/"
	header := http.Header{}
	header.Set("Authorization", "bearer "+p.cfg.APIKey)
	header.Set("X-DashScope-DataInspection", "enable")
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			return Audio{}, upstreamError("阿里云百炼", resp)
		}
		return Audio{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(time.Duration(p.cfg.timeoutOr(60)) * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	taskID := strings.ReplaceAll(newUUID(), "-", "")
	head := func(action string) map[string]any {
		return map[string]any{"action": action, "task_id": taskID, "streaming": "duplex"}
	}
	if err := conn.WriteJSON(map[string]any{
		"header": head("run-task"),
		"payload": map[string]any{
			"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer", "model": p.model(),
			"parameters": map[string]any{
				"text_type": "PlainText", "voice": voice, "format": "mp3", "sample_rate": 22050,
				"volume": 50, "rate": 1, "pitch": 1,
			},
			"input": map[string]any{},
		},
	}); err != nil {
		return Audio{}, err
	}

	var audio bytes.Buffer
	started := false
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return Audio{}, ctx.Err()
			}
			return Audio{}, &ProviderError{Provider: "阿里云百炼", Message: "连接中断：" + err.Error()}
		}
		if kind == websocket.BinaryMessage {
			if audio.Len()+len(data) > maxAudioBytes {
				return Audio{}, &ProviderError{Provider: "阿里云百炼", Message: "音频过大"}
			}
			audio.Write(data)
			continue
		}
		var ev dashScopeEvent
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Header.Event {
		case "task-started":
			if started {
				continue
			}
			started = true
			if err := conn.WriteJSON(map[string]any{"header": head("continue-task"), "payload": map[string]any{"input": map[string]any{"text": text}}}); err != nil {
				return Audio{}, err
			}
			if err := conn.WriteJSON(map[string]any{"header": head("finish-task"), "payload": map[string]any{"input": map[string]any{}}}); err != nil {
				return Audio{}, err
			}
		case "task-finished":
			if audio.Len() == 0 {
				return Audio{}, &ProviderError{Provider: "阿里云百炼", Message: "返回空音频"}
			}
			return Audio{Data: audio.Bytes(), ContentType: "audio/mpeg"}, nil
		case "task-failed":
			return Audio{}, &ProviderError{Provider: "阿里云百炼", Message: strings.TrimSpace(ev.Header.ErrorCode + " " + ev.Header.ErrorMessage)}
		}
	}
}
