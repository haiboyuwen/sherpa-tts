package tts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Audio 是一段合成结果。
type Audio struct {
	Data        []byte
	ContentType string // audio/mpeg、audio/wav …
}

// Provider 是一家 TTS 服务。voice 为提供商内的音色 ID。
type Provider interface {
	Voices(ctx context.Context) ([]VoiceOption, error)
	Synthesize(ctx context.Context, voice, text string) (Audio, error)
}

// NewProvider 按配置构造提供商。hc 为空时使用按配置超时的默认客户端。
func NewProvider(cfg ProviderConfig, hc *http.Client) (Provider, error) {
	if hc == nil {
		hc = &http.Client{Timeout: time.Duration(cfg.timeoutOr(60)) * time.Second}
	}
	switch cfg.Type {
	case TypeSherpa:
		return &sherpaProvider{cfg: cfg, hc: hc}, nil
	case TypeOpenAI:
		return &openAIProvider{cfg: cfg, hc: hc}, nil
	case TypeAzure:
		return &azureProvider{cfg: cfg, hc: hc}, nil
	case TypeDashScope:
		return &dashScopeProvider{cfg: cfg, hc: hc}, nil
	case TypeVolcengine:
		return &volcengineProvider{cfg: cfg, hc: hc}, nil
	}
	return nil, fmt.Errorf("未知的 TTS 提供商类型 %q", cfg.Type)
}

// ProviderError 是上游返回的错误，Status 为上游 HTTP 状态码（0 表示协议层错误）。
type ProviderError struct {
	Provider string
	Status   int
	Message  string
}

func (e *ProviderError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s：HTTP %d %s", e.Provider, e.Status, e.Message)
	}
	return e.Provider + "：" + e.Message
}

// upstreamError 读取失败响应的前 512 字节作为错误说明。
func upstreamError(provider string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &ProviderError{Provider: provider, Status: resp.StatusCode, Message: strings.TrimSpace(string(body))}
}

const maxAudioBytes = 32 << 20

func readAudio(provider string, resp *http.Response, fallbackType string) (Audio, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes))
	if err != nil {
		return Audio{}, err
	}
	if len(data) == 0 {
		return Audio{}, &ProviderError{Provider: provider, Message: "返回空音频"}
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "audio/") {
		ct = fallbackType
	}
	return Audio{Data: data, ContentType: ct}, nil
}

// configuredVoices 返回用户配置的音色，没配就用类型默认值。
func configuredVoices(cfg ProviderConfig) []VoiceOption {
	if len(cfg.Voices) > 0 {
		return cfg.Voices
	}
	return defaultVoices(cfg.Type)
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

var errEmptyText = errors.New("没有可合成的文字")
