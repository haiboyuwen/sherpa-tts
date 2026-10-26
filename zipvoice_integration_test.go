package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// 仅显式提供已有模型时运行；不会下载模型或读取运行中的服务配置。
func TestZipvoiceNativeChunkControl(t *testing.T) {
	dir := os.Getenv("SHERPA_TTS_TEST_MODEL_DIR")
	if dir == "" {
		t.Skip("设置 SHERPA_TTS_TEST_MODEL_DIR 可复用已有模型验证原生分块")
	}
	tts := sherpa.NewOfflineTts(specs["zipvoice"].config(dir, 4))
	if tts == nil {
		t.Fatal("加载本地 ZipVoice 模型失败")
	}
	defer sherpa.DeleteOfflineTts(tts)
	e := &engine{reg: &registry{voicesDir: "voices"}}
	samples, sampleRate, referenceText, err := e.resolveReference("晓晓")
	if err != nil {
		t.Fatal(err)
	}
	reference := sherpa.GenerationConfig{
		Speed: 1, NumSteps: 4, ReferenceAudio: samples,
		ReferenceSampleRate: sampleRate, ReferenceText: referenceText,
	}
	outDir := os.Getenv("SHERPA_TTS_TEST_OUTPUT_DIR")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "夜晚，陈浔与大黑牛回到院子中。\n他将这一年的长生点依旧加在了速度中，一人一牛鬼鬼祟祟的围在一起。\n“老牛，刨！”\n哞！\n土块翻飞，渐渐露出了一个土罐，里面全是铜钱，还有一块银子，陈浔那个感慨啊，不断长嘘短叹，有钱了。"
	cases := []struct {
		name string
		text string
	}{
		{"原例短尾", "他将这一年的长生点依旧加在了速度中，一人一牛鬼鬼祟祟的围在一起。“老牛，刨！”哞！"},
		{"长文本", strings.Repeat(original+"\n", 2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := normalize(tc.text)
			calls, callbacks := 0, 0
			audio, err := generateZipvoice(text, reference, func(chunk string, config *sherpa.GenerationConfig) *sherpa.GeneratedAudio {
				calls++
				before := callbacks
				generated := tts.GenerateWithConfig(chunk, config, func(samples []float32, progress float32) bool {
					callbacks++
					return true
				})
				if callbacks-before != 1 {
					t.Errorf("一个应用层块被原生拆成了 %d 块：%q", callbacks-before, chunk)
				}
				return generated
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != len(splitZipvoiceText(text)) || callbacks != calls || audio.SampleRate != 24000 {
				t.Fatalf("生成次数/采样率不正确：calls=%d callbacks=%d rate=%d", calls, callbacks, audio.SampleRate)
			}
			data, err := encodeMP3(audio.Samples, audio.SampleRate)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(outDir, tc.name+".mp3")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("应用层调用=%d，实际原生块=%d，音频=%s", calls, callbacks, path)
		})
	}
}
