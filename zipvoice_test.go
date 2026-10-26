package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

func TestSplitZipvoiceText(t *testing.T) {
	cases := []string{
		normalize("他将这一年的长生点依旧加在了速度中，一人一牛鬼鬼祟祟的围在一起。“老牛，刨！”哞！"),
		"“老牛，刨！”\n哞！\n土块翻飞，渐渐露出了一个土罐。",
		"甲！" + strings.Repeat("乙", 208),
		strings.Repeat("one two three. ", 36),
		strings.Repeat("a", 401),
		strings.Repeat("🙂甲", 201),
		strings.Repeat("123 456 789; ", 45),
	}
	for _, n := range []int{1, 29, 30, 31, 80, 81, 199, 200, 201, 399, 400, 401, 599, 600} {
		cases = append(cases, strings.Repeat("甲", n))
	}
	for i, text := range cases {
		t.Run(fmt.Sprintf("case%d-runes%d", i, utf8.RuneCountInString(text)), func(t *testing.T) {
			chunks := splitZipvoiceText(text)
			if strings.Join(chunks, "") != text {
				t.Fatal("分块丢失或重复了原文")
			}
			want := (utf8.RuneCountInString(text) + zipvoiceChunkMax - 1) / zipvoiceChunkMax
			if len(chunks) != want {
				t.Fatalf("块数 = %d，期望 %d", len(chunks), want)
			}
			for _, chunk := range chunks {
				n := utf8.RuneCountInString(chunk)
				if n == 0 || n > zipvoiceChunkMax || len(chunks) > 1 && n < zipvoiceChunkMin {
					t.Fatalf("无效块长度 %d", n)
				}
				if !utf8.ValidString(chunk) {
					t.Fatal("分块破坏 UTF-8")
				}
			}
		})
	}
	if got := splitZipvoiceText(""); len(got) != 0 {
		t.Fatalf("空文本不应产生块：%v", got)
	}
}

func TestSplitZipvoiceTextPrefersNearbyWordBoundary(t *testing.T) {
	left, right := strings.Repeat("甲", 100)+"。", strings.Repeat("乙", 100)
	if got := splitZipvoiceText(left + right); !slices.Equal(got, []string{left, right}) {
		t.Fatalf("应在相邻句尾切分：%v", got)
	}
}

func TestGenerateZipvoiceControlsNativeChunks(t *testing.T) {
	reference := sherpa.GenerationConfig{
		Speed: 1, NumSteps: 4, ReferenceAudio: []float32{0.1, 0.2},
		ReferenceSampleRate: 16000, ReferenceText: "参考文本",
	}
	texts := []string{normalize("他将这一年的长生点依旧加在了速度中，一人一牛鬼鬼祟祟的围在一起。“老牛，刨！”哞！")}
	for _, n := range []int{1, 30, 31, 39, 80, 81, 199, 200, 201, 401, 600} {
		texts = append(texts, strings.Repeat("甲", n))
	}
	for _, text := range texts {
		n := utf8.RuneCountInString(text)
		var sent []string
		var expected []float32
		audio, err := generateZipvoice(text, reference, func(chunk string, config *sherpa.GenerationConfig) *sherpa.GeneratedAudio {
			sent = append(sent, chunk)
			var extra struct {
				Min int `json:"min_char_in_sentence"`
				Max int `json:"max_char_in_sentence"`
			}
			if err := json.Unmarshal(config.Extra, &extra); err != nil {
				t.Fatalf("非法 Extra：%v", err)
			}
			if extra.Min != utf8.RuneCountInString(chunk)+1 || extra.Max != zipvoiceChunkMax {
				t.Fatalf("原生分句配置不匹配：%s", config.Extra)
			}
			if config.Speed != reference.Speed || config.NumSteps != reference.NumSteps || config.ReferenceSampleRate != reference.ReferenceSampleRate || config.ReferenceText != reference.ReferenceText || !slices.Equal(config.ReferenceAudio, reference.ReferenceAudio) {
				t.Fatal("速度、步数或参考素材被改变")
			}
			samples := []float32{float32(len(sent)) / 10, -float32(len(sent)) / 10}
			expected = append(expected, samples...)
			return &sherpa.GeneratedAudio{Samples: samples, SampleRate: 24000}
		})
		if err != nil {
			t.Fatalf("长度 %d：%v", n, err)
		}
		if strings.Join(sent, "") != text || len(sent) != (n+zipvoiceChunkMax-1)/zipvoiceChunkMax {
			t.Fatalf("长度 %d 的调用分块不正确：%v", n, sent)
		}
		if audio.SampleRate != 24000 || !slices.Equal(audio.Samples, expected) {
			t.Fatalf("PCM 拼接不正确：%+v", audio)
		}
	}
}

func TestGenerateZipvoiceRejectsPartialAudio(t *testing.T) {
	cases := []struct {
		name   string
		second *sherpa.GeneratedAudio
	}{
		{"nil", nil},
		{"empty", &sherpa.GeneratedAudio{SampleRate: 24000}},
		{"zero rate", &sherpa.GeneratedAudio{Samples: []float32{0.1}}},
		{"negative rate", &sherpa.GeneratedAudio{Samples: []float32{0.1}, SampleRate: -1}},
		{"different rate", &sherpa.GeneratedAudio{Samples: []float32{0.1}, SampleRate: 16000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			audio, err := generateZipvoice(strings.Repeat("甲", 401), sherpa.GenerationConfig{}, func(string, *sherpa.GenerationConfig) *sherpa.GeneratedAudio {
				calls++
				if calls == 1 {
					return &sherpa.GeneratedAudio{Samples: []float32{0.1}, SampleRate: 24000}
				}
				return tc.second
			})
			if err == nil || audio != nil || calls != 2 {
				t.Fatalf("不能返回或继续生成部分结果：audio=%+v err=%v calls=%d", audio, err, calls)
			}
		})
	}
	if audio, err := generateZipvoice(" \n ", sherpa.GenerationConfig{}, func(string, *sherpa.GenerationConfig) *sherpa.GeneratedAudio {
		t.Fatal("空文本不应调用模型")
		return nil
	}); err != errEmptyText || audio != nil {
		t.Fatalf("空文本：%v %v", audio, err)
	}
}
