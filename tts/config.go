// Package tts 是多家语音合成服务的统一客户端：本地 sherpa-tts 服务、OpenAI 兼容接口、
// Azure 语音、阿里云百炼（CosyVoice / Qwen-TTS）、火山引擎豆包语音。
//
// 应用后端用 [Service] 汇总所有已启用提供商的音色、按「提供商:音色」合成并做磁盘缓存，
// 再把 [Service.VoicesHandler]、[Service.SynthesizeHandler] 挂到自己的路由上；
// 管理后台用 [Config.View]、[MergeUpdate]、[TestProvider] 和 [ProviderTypes] 做配置页。
// 纯 Go，无 CGO。
package tts

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 提供商类型。
const (
	TypeSherpa     = "sherpa"     // 本地 sherpa-tts 服务（/health + /synthesize）
	TypeOpenAI     = "openai"     // OpenAI /v1/audio/speech 及兼容实现
	TypeAzure      = "azure"      // Azure AI Speech REST（SSML）
	TypeDashScope  = "dashscope"  // 阿里云百炼：CosyVoice（WebSocket）/ Qwen-TTS（HTTP）
	TypeVolcengine = "volcengine" // 火山引擎豆包语音合成 HTTP v1
)

// VoiceOption 是提供商内的一个音色；ID 为提供商自己的音色标识。
type VoiceOption struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// ProviderConfig 是一个提供商实例。同一类型可以配置多个（如两个 OpenAI 兼容端点），用 ID 区分。
type ProviderConfig struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`

	BaseURL      string `json:"base_url,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	Model        string `json:"model,omitempty"`
	Region       string `json:"region,omitempty"`       // Azure
	AppID        string `json:"app_id,omitempty"`       // 火山引擎
	Cluster      string `json:"cluster,omitempty"`      // 火山引擎
	Instructions string `json:"instructions,omitempty"` // OpenAI gpt-4o-mini-tts 的朗读风格提示

	// Voices 为空时用提供商的默认音色（sherpa、Azure 会在线查询）。
	Voices         []VoiceOption `json:"voices,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`

	// ClearAPIKey 只出现在后台提交里：true 表示删除已保存的密钥；保存前会被清掉。
	ClearAPIKey bool `json:"clear_api_key,omitempty"`
}

// Config 是应用的全部 TTS 配置。
type Config struct {
	Providers []ProviderConfig `json:"providers"`
	// DefaultVoice 是全局音色 ID（「提供商ID:音色」），前端「自动」时优先用它。
	DefaultVoice string `json:"default_voice,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// DisplayName 返回后台和音色标签里使用的提供商名称。
func (p ProviderConfig) DisplayName() string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	if meta, ok := typeMeta(p.Type); ok {
		return meta.Label
	}
	return p.ID
}

// Normalize 去掉首尾空白、补默认值，并丢弃后台专用的瞬时字段。
func (c Config) Normalize() Config {
	out := Config{DefaultVoice: strings.TrimSpace(c.DefaultVoice), Providers: make([]ProviderConfig, 0, len(c.Providers))}
	for _, p := range c.Providers {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		p.Type = strings.ToLower(strings.TrimSpace(p.Type))
		p.Name = strings.TrimSpace(p.Name)
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		p.APIKey = strings.TrimSpace(p.APIKey)
		p.Model = strings.TrimSpace(p.Model)
		p.Region = strings.TrimSpace(p.Region)
		p.AppID = strings.TrimSpace(p.AppID)
		p.Cluster = strings.TrimSpace(p.Cluster)
		p.Instructions = strings.TrimSpace(p.Instructions)
		p.ClearAPIKey = false
		voices := make([]VoiceOption, 0, len(p.Voices))
		for _, v := range p.Voices {
			v.ID, v.Label = strings.TrimSpace(v.ID), strings.TrimSpace(v.Label)
			if v.ID != "" {
				voices = append(voices, v)
			}
		}
		p.Voices = voices
		if p.TimeoutSeconds < 0 {
			p.TimeoutSeconds = 0
		}
		out.Providers = append(out.Providers, p)
	}
	return out
}

// Validate 检查 ID 唯一、类型已知、必填项齐全。
func (c Config) Validate() error {
	seen := map[string]bool{}
	for i, p := range c.Providers {
		where := fmt.Sprintf("第 %d 个提供商", i+1)
		if !idPattern.MatchString(p.ID) {
			return fmt.Errorf("%s：ID 只能用小写字母、数字、- 和 _（1–32 位）", where)
		}
		if seen[p.ID] {
			return fmt.Errorf("提供商 ID 重复：%s", p.ID)
		}
		seen[p.ID] = true
		meta, ok := typeMeta(p.Type)
		if !ok {
			return fmt.Errorf("%s（%s）：未知类型 %q", where, p.ID, p.Type)
		}
		if p.Disabled {
			continue
		}
		for _, field := range meta.Fields {
			if field.Required && strings.TrimSpace(fieldValue(p, field.Key)) == "" {
				return fmt.Errorf("%s（%s）：缺少%s", where, p.ID, field.Label)
			}
		}
		if p.BaseURL != "" {
			u, err := url.Parse(p.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
				return fmt.Errorf("%s（%s）：服务地址必须是有效的 HTTP/HTTPS URL，且不能包含用户名或密码", where, p.ID)
			}
		}
		if p.Type == TypeAzure && p.Region == "" && p.BaseURL == "" {
			return fmt.Errorf("%s（%s）：Azure 需要区域或自定义端点", where, p.ID)
		}
	}
	return nil
}

func fieldValue(p ProviderConfig, key string) string {
	switch key {
	case "base_url":
		return p.BaseURL
	case "api_key":
		return p.APIKey
	case "model":
		return p.Model
	case "region":
		return p.Region
	case "app_id":
		return p.AppID
	case "cluster":
		return p.Cluster
	case "instructions":
		return p.Instructions
	}
	return ""
}

// ProviderView 是返回给后台的提供商配置：密钥只给掩码。
type ProviderView struct {
	ProviderConfig
	HasAPIKey  bool   `json:"has_api_key"`
	APIKeyHint string `json:"api_key_hint,omitempty"`
}

// ConfigView 是返回给后台的整体配置。
type ConfigView struct {
	Providers    []ProviderView `json:"providers"`
	DefaultVoice string         `json:"default_voice,omitempty"`
}

// View 去掉密钥明文，供后台 GET 使用。
func (c Config) View() ConfigView {
	view := ConfigView{DefaultVoice: c.DefaultVoice, Providers: make([]ProviderView, 0, len(c.Providers))}
	for _, p := range c.Providers {
		item := ProviderView{ProviderConfig: p, HasAPIKey: p.APIKey != "", APIKeyHint: secretHint(p.APIKey)}
		item.APIKey = ""
		view.Providers = append(view.Providers, item)
	}
	return view
}

func secretHint(secret string) string {
	if secret == "" {
		return ""
	}
	r := []rune(secret)
	if len(r) <= 8 {
		return "已设置"
	}
	return string(r[:3]) + "…" + string(r[len(r)-4:])
}

// MergeUpdate 把后台提交的配置合并到旧配置上：同 ID 的提供商密钥留空表示不改，
// clear_api_key=true 表示删除。返回规范化后的新配置。
func MergeUpdate(prev, next Config) Config {
	old := map[string]ProviderConfig{}
	for _, p := range prev.Normalize().Providers {
		old[p.ID] = p
	}
	for i, p := range next.Providers {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		switch {
		case p.ClearAPIKey:
			next.Providers[i].APIKey = ""
		case strings.TrimSpace(p.APIKey) == "":
			if before, ok := old[id]; ok {
				next.Providers[i].APIKey = before.APIKey
			}
		}
	}
	return next.Normalize()
}

// ResolveSecret 用于后台「测试」：表单里密钥留空时沿用已保存的同 ID 提供商密钥。
func ResolveSecret(saved Config, p ProviderConfig) ProviderConfig {
	merged := MergeUpdate(saved, Config{Providers: []ProviderConfig{p}})
	return merged.Providers[0]
}

// LoadFile 读取 JSON 配置文件；文件不存在时 ok=false。
func LoadFile(path string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("解析 %s：%w", path, err)
	}
	return cfg.Normalize(), true, nil
}

// SaveFile 以 0600 原子写入 JSON 配置（含密钥）。
func SaveFile(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ConfigFromEnv 从环境变量构造配置，供首次启动或未保存后台配置时使用。prefix 如 "TTS_"、"MEDIAVAULT_TTS_"。
//
//	{prefix}URL                         本地 sherpa-tts 服务地址 → 提供商 local
//	{prefix}OPENAI_API_KEY / _BASE_URL / _MODEL / _VOICES / _INSTRUCTIONS
//	{prefix}AZURE_API_KEY / _REGION / _BASE_URL / _VOICES
//	{prefix}DASHSCOPE_API_KEY / _MODEL / _BASE_URL / _VOICES
//	{prefix}VOLCENGINE_APP_ID / _ACCESS_TOKEN / _CLUSTER / _VOICES
//	{prefix}DEFAULT_VOICE               全局默认音色，如 azure:zh-CN-XiaoxiaoNeural
//
// *_VOICES 为逗号分隔的「音色ID=显示名」列表，显示名可省略。
func ConfigFromEnv(prefix string, getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := func(key string) string { return strings.TrimSpace(getenv(prefix + key)) }
	var cfg Config
	if url := env("URL"); url != "" {
		cfg.Providers = append(cfg.Providers, ProviderConfig{ID: "local", Type: TypeSherpa, Name: "本地 sherpa-tts", BaseURL: url})
	}
	if key, base := env("OPENAI_API_KEY"), env("OPENAI_BASE_URL"); key != "" || base != "" {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID: "openai", Type: TypeOpenAI, APIKey: key, BaseURL: base, Model: env("OPENAI_MODEL"),
			Instructions: env("OPENAI_INSTRUCTIONS"), Voices: ParseVoiceList(env("OPENAI_VOICES")),
		})
	}
	if key := env("AZURE_API_KEY"); key != "" {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID: "azure", Type: TypeAzure, APIKey: key, Region: env("AZURE_REGION"), BaseURL: env("AZURE_BASE_URL"),
			Voices: ParseVoiceList(env("AZURE_VOICES")),
		})
	}
	if key := env("DASHSCOPE_API_KEY"); key != "" {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID: "dashscope", Type: TypeDashScope, APIKey: key, Model: env("DASHSCOPE_MODEL"), BaseURL: env("DASHSCOPE_BASE_URL"),
			Voices: ParseVoiceList(env("DASHSCOPE_VOICES")),
		})
	}
	if app, token := env("VOLCENGINE_APP_ID"), env("VOLCENGINE_ACCESS_TOKEN"); app != "" && token != "" {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID: "volcengine", Type: TypeVolcengine, AppID: app, APIKey: token, Cluster: env("VOLCENGINE_CLUSTER"),
			Voices: ParseVoiceList(env("VOLCENGINE_VOICES")),
		})
	}
	cfg.DefaultVoice = env("DEFAULT_VOICE")
	return cfg.Normalize()
}

// ParseVoiceList 解析「id=显示名,id2」形式的音色列表。
func ParseVoiceList(raw string) []VoiceOption {
	var out []VoiceOption
	for _, item := range strings.Split(raw, ",") {
		id, label, _ := strings.Cut(strings.TrimSpace(item), "=")
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, VoiceOption{ID: id, Label: strings.TrimSpace(label)})
		}
	}
	return out
}

func (p ProviderConfig) timeoutOr(fallback int) int {
	if p.TimeoutSeconds > 0 {
		return p.TimeoutSeconds
	}
	return fallback
}
