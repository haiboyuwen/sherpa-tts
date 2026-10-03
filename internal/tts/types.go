package tts

// FieldMeta 描述后台表单里的一个字段；后台按它渲染，不必为每家提供商写死表单。
type FieldMeta struct {
	Key         string `json:"key"` // base_url / api_key / model / region / app_id / cluster / instructions
	Label       string `json:"label"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

// TypeMeta 描述一种提供商类型。
type TypeMeta struct {
	Type   string        `json:"type"`
	Label  string        `json:"label"`
	Help   string        `json:"help,omitempty"`
	DocURL string        `json:"doc_url,omitempty"`
	Fields []FieldMeta   `json:"fields"`
	Voices []VoiceOption `json:"default_voices,omitempty"` // 留空音色列表时使用的默认音色（Azure 另会在线查询）
	// VoicesOnline 为 true 表示不填音色时会向服务在线查询。
	VoicesOnline bool `json:"voices_online,omitempty"`
}

var providerTypes = []TypeMeta{
	{
		Type:   TypeOpenAI,
		Label:  "OpenAI 兼容",
		Help:   "OpenAI /v1/audio/speech；硅基流动、Kokoro-FastAPI、openedai-speech 等兼容服务改服务地址、模型和音色即可。",
		DocURL: "https://platform.openai.com/docs/guides/text-to-speech",
		Fields: []FieldMeta{
			{Key: "base_url", Label: "服务地址", Placeholder: "https://api.openai.com/v1"},
			{Key: "api_key", Label: "API Key", Secret: true, Help: "自建兼容服务可留空"},
			{Key: "model", Label: "模型", Placeholder: "gpt-4o-mini-tts"},
			{Key: "instructions", Label: "朗读风格", Placeholder: "用平稳庄重的语气朗读中文古文", Help: "仅 gpt-4o-mini-tts 等支持指令的模型生效"},
		},
		Voices: []VoiceOption{
			{ID: "alloy", Label: "Alloy"}, {ID: "ash", Label: "Ash"}, {ID: "ballad", Label: "Ballad"},
			{ID: "coral", Label: "Coral"}, {ID: "echo", Label: "Echo"}, {ID: "fable", Label: "Fable"},
			{ID: "nova", Label: "Nova"}, {ID: "onyx", Label: "Onyx"}, {ID: "sage", Label: "Sage"},
			{ID: "shimmer", Label: "Shimmer"}, {ID: "verse", Label: "Verse"},
		},
	},
	{
		Type:   TypeAzure,
		Label:  "Azure 语音",
		Help:   "微软 Azure AI Speech 神经网络音色。不填音色时自动列出全部中文音色。",
		DocURL: "https://learn.microsoft.com/azure/ai-services/speech-service/rest-text-to-speech",
		Fields: []FieldMeta{
			{Key: "api_key", Label: "资源密钥", Required: true, Secret: true},
			{Key: "region", Label: "区域", Placeholder: "eastasia", Help: "与自定义端点二选一"},
			{Key: "base_url", Label: "自定义端点", Placeholder: "https://eastasia.tts.speech.microsoft.com"},
		},
		Voices: []VoiceOption{
			{ID: "zh-CN-XiaoxiaoNeural", Label: "晓晓"}, {ID: "zh-CN-YunxiNeural", Label: "云希"},
			{ID: "zh-CN-YunjianNeural", Label: "云健"}, {ID: "zh-CN-XiaoyiNeural", Label: "晓伊"},
			{ID: "zh-CN-YunyangNeural", Label: "云扬"},
		},
		VoicesOnline: true,
	},
	{
		Type:   TypeDashScope,
		Label:  "阿里云百炼",
		Help:   "CosyVoice（cosyvoice-v2 等，WebSocket）或 Qwen-TTS（qwen-tts、qwen3-tts-flash，HTTP）；按模型名自动选择协议。",
		DocURL: "https://help.aliyun.com/zh/model-studio/cosyvoice-websocket-api",
		Fields: []FieldMeta{
			{Key: "api_key", Label: "API Key", Required: true, Secret: true},
			{Key: "model", Label: "模型", Placeholder: "cosyvoice-v2"},
			{Key: "base_url", Label: "服务地址", Placeholder: "https://dashscope.aliyuncs.com", Help: "国际站用 https://dashscope-intl.aliyuncs.com"},
		},
		Voices: []VoiceOption{
			{ID: "longxiaochun_v2", Label: "龙小淳"}, {ID: "longwan_v2", Label: "龙婉"},
			{ID: "longcheng_v2", Label: "龙橙"}, {ID: "longhua_v2", Label: "龙华"},
			{ID: "longshu_v2", Label: "龙书"}, {ID: "longxiaoxia_v2", Label: "龙小夏"},
		},
	},
	{
		Type:   TypeVolcengine,
		Label:  "火山引擎豆包",
		Help:   "火山引擎豆包语音合成（HTTP 非流式）。大模型音色集群用 volcano_tts。",
		DocURL: "https://www.volcengine.com/docs/6561/79820",
		Fields: []FieldMeta{
			{Key: "app_id", Label: "App ID", Required: true},
			{Key: "api_key", Label: "Access Token", Required: true, Secret: true},
			{Key: "cluster", Label: "集群", Placeholder: "volcano_tts"},
			{Key: "base_url", Label: "服务地址", Placeholder: "https://openspeech.bytedance.com"},
		},
		Voices: []VoiceOption{
			{ID: "zh_female_shuangkuaisisi_moon_bigtts", Label: "爽快思思"},
			{ID: "zh_male_wennuanahu_moon_bigtts", Label: "温暖阿虎"},
			{ID: "BV001_streaming", Label: "通用女声"},
			{ID: "BV002_streaming", Label: "通用男声"},
		},
	},
}

// ProviderTypes 返回所有支持的提供商类型及其表单字段，供后台渲染。
func ProviderTypes() []TypeMeta {
	out := make([]TypeMeta, len(providerTypes))
	copy(out, providerTypes)
	return out
}

func typeMeta(t string) (TypeMeta, bool) {
	for _, m := range providerTypes {
		if m.Type == t {
			return m, true
		}
	}
	return TypeMeta{}, false
}

func defaultVoices(t string) []VoiceOption {
	meta, _ := typeMeta(t)
	return meta.Voices
}
