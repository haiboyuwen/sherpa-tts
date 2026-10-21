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

// qwen3DefaultVoices 是 qwen3-tts-flash 的系统音色（文档：qwen-tts-voice-list），
// 后 10 个是方言音色，同时支持普通话等标准语种。
var qwen3DefaultVoices = []VoiceOption{
	{ID: "Cherry", Label: "Cherry 芊悦 · 女 · 阳光亲切的姐姐"},
	{ID: "Serena", Label: "Serena 苏瑶 · 女 · 温柔的姐姐"},
	{ID: "Ethan", Label: "Ethan 晨煦 · 男 · 标准普通话带北方口音，阳光温暖"},
	{ID: "Chelsie", Label: "Chelsie 千雪 · 女 · 二次元虚拟女友"},
	{ID: "Momo", Label: "Momo 茉兔 · 女 · 撒娇搞怪"},
	{ID: "Vivian", Label: "Vivian 十三 · 女 · 拽拽的小暴躁"},
	{ID: "Moon", Label: "Moon 月白 · 男 · 率性帅气"},
	{ID: "Maia", Label: "Maia 四月 · 女 · 知性温柔"},
	{ID: "Kai", Label: "Kai 凯 · 男 · 耳朵的SPA"},
	{ID: "Nofish", Label: "Nofish 不吃鱼 · 男 · 不卷舌的设计师"},
	{ID: "Bella", Label: "Bella 萌宝 · 女 · 小萝莉"},
	{ID: "Jennifer", Label: "Jennifer 詹妮弗 · 女 · 电影质感美式女声"},
	{ID: "Ryan", Label: "Ryan 甜茶 · 男 · 戏感饱满，张力十足"},
	{ID: "Katerina", Label: "Katerina 卡捷琳娜 · 女 · 御姐，韵律感强"},
	{ID: "Aiden", Label: "Aiden 艾登 · 男 · 美式大男孩"},
	{ID: "Eldric Sage", Label: "Eldric Sage 沧明子 · 男 · 沉稳睿智的老者"},
	{ID: "Mia", Label: "Mia 乖小妹 · 女 · 温顺乖巧"},
	{ID: "Mochi", Label: "Mochi 沙小弥 · 男 · 早慧的小大人"},
	{ID: "Bellona", Label: "Bellona 燕铮莺 · 女 · 洪亮清晰，适合武侠叙事"},
	{ID: "Vincent", Label: "Vincent 田叔 · 男 · 沙哑烟嗓，江湖豪情"},
	{ID: "Bunny", Label: "Bunny 萌小姬 · 女 · 萌系小萝莉"},
	{ID: "Neil", Label: "Neil 阿闻 · 男 · 新闻主持风格"},
	{ID: "Elias", Label: "Elias 墨讲师 · 女 · 严谨的讲师"},
	{ID: "Arthur", Label: "Arthur 徐大爷 · 男 · 质朴嗓音，讲乡间奇闻"},
	{ID: "Nini", Label: "Nini 邻家妹妹 · 女 · 软糯甜美"},
	{ID: "Seren", Label: "Seren 小婉 · 女 · 温和舒缓，适合助眠"},
	{ID: "Pip", Label: "Pip 顽屁小孩 · 男 · 调皮的小孩"},
	{ID: "Stella", Label: "Stella 少女阿月 · 女 · 甜美迷糊的少女"},
	{ID: "Bodega", Label: "Bodega 博德加 · 男 · 热情的西班牙大叔"},
	{ID: "Sonrisa", Label: "Sonrisa 索尼莎 · 女 · 开朗的拉美大姐"},
	{ID: "Alek", Label: "Alek 阿列克 · 男 · 俄式，外冷内热"},
	{ID: "Dolce", Label: "Dolce 多尔切 · 男 · 慵懒的意大利大叔"},
	{ID: "Sohee", Label: "Sohee 素熙 · 女 · 温柔的韩国姐姐"},
	{ID: "Ono Anna", Label: "Ono Anna 小野杏 · 女 · 鬼灵精怪的青梅竹马"},
	{ID: "Lenn", Label: "Lenn 莱恩 · 男 · 理性的德国青年"},
	{ID: "Emilien", Label: "Emilien 埃米尔安 · 男 · 浪漫的法国大哥哥"},
	{ID: "Andre", Label: "Andre 安德雷 · 男 · 磁性沉稳"},
	{ID: "Radio Gol", Label: "Radio Gol 拉迪奥·戈尔 · 男 · 足球解说"},
	{ID: "Jada", Label: "Jada 上海-阿珍 · 女 · 上海话"},
	{ID: "Dylan", Label: "Dylan 北京-晓东 · 男 · 北京话"},
	{ID: "Li", Label: "Li 南京-老李 · 男 · 南京话"},
	{ID: "Marcus", Label: "Marcus 陕西-秦川 · 男 · 陕西话"},
	{ID: "Roy", Label: "Roy 闽南-阿杰 · 男 · 闽南语"},
	{ID: "Peter", Label: "Peter 天津-李彼得 · 男 · 天津话"},
	{ID: "Sunny", Label: "Sunny 四川-晴儿 · 女 · 四川话"},
	{ID: "Eric", Label: "Eric 四川-程川 · 男 · 四川话"},
	{ID: "Rocky", Label: "Rocky 粤语-阿强 · 男 · 粤语"},
	{ID: "Kiki", Label: "Kiki 粤语-阿清 · 女 · 粤语"},
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
		if strings.HasPrefix(strings.ToLower(p.model()), "qwen3-tts") {
			return qwen3DefaultVoices, nil
		}
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
