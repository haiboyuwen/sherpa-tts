package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/haiboyuwen/sherpa-tts/internal/tts"
)

func TestZipvoiceFeatScale(t *testing.T) {
	cfg := specs["zipvoice"].config("/models/zipvoice", 4)
	if got := cfg.Model.Zipvoice.FeatScale; got != 0.1 {
		t.Fatalf("ZipVoice FeatScale 应为上游默认值 0.1，得到 %v", got)
	}
}

func writeVoiceFixture(t *testing.T, dir, name, text string) {
	t.Helper()
	pcm := make([]int16, 2*16000)
	for i := range pcm {
		pcm[i] = int16(16000 * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	if err := os.WriteFile(filepath.Join(dir, name+".wav"), buildWAV(t, 1, 16, pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestZipvoiceDirectoryVoices(t *testing.T) {
	dir := t.TempDir()
	e := &engine{spec: specs["zipvoice"], reg: &registry{voicesDir: dir}}
	if got := e.listVoices(); len(got) != 0 {
		t.Fatalf("空目录不应提供示例音色：%v", got)
	}
	writeVoiceFixture(t, dir, "晓晓", "测试声音")
	writeVoiceFixture(t, dir, "云希", "测试声音")
	writeVoiceFixture(t, dir, "leijun", "本地声音")
	writeVoiceFixture(t, dir, "空文本", " \n\t ")
	writeVoiceFixture(t, dir, ".隐藏", "测试声音")
	writeVoiceFixture(t, dir, "缺文本", "测试声音")
	if err := os.Remove(filepath.Join(dir, "缺文本.txt")); err != nil {
		t.Fatal(err)
	}
	writeVoiceFixture(t, dir, "无效音频", "测试声音")
	if err := os.WriteFile(filepath.Join(dir, "无效音频.wav"), []byte("not a wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeVoiceFixture(t, dir, "文本目录", "测试声音")
	if err := os.Remove(filepath.Join(dir, "文本目录.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "文本目录.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "缺音频.txt"), []byte("测试声音"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, voice := range e.listVoices() {
		got = append(got, voice.Label)
	}
	want := []string{"leijun", "云希", "晓晓"}
	if !slices.Equal(got, want) {
		t.Fatalf("只应列出有效文件对：got=%v want=%v", got, want)
	}
	if _, _, _, err := e.resolveReference("空文本"); err == nil {
		t.Fatal("空白参考文本应被拒绝")
	}
	if _, sr, text, err := e.resolveReference("leijun"); err != nil || sr != 16000 || text != "本地声音" {
		t.Fatalf("同名音色应读取目录素材：sr=%d text=%q err=%v", sr, text, err)
	}
}

func TestZipvoiceNoModelSampleFallback(t *testing.T) {
	root := t.TempDir()
	reg := newRegistry(filepath.Join(root, "models"), filepath.Join(root, "voices"), "", 1, []string{"zipvoice"})
	e := reg.engines["zipvoice"]
	if got := e.listVoices(); len(got) != 0 {
		t.Fatalf("目录不存在时不应回退示例：%v", got)
	}
	sampleDir := filepath.Join(reg.modelDir, e.spec.archive, "test_wavs")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "leijun-1.wav"), buildWAV(t, 1, 16, make([]int16, 16000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.resolveReference("leijun"); err == nil {
		t.Fatal("不能从模型包绕过目录读取示例音色")
	}
}

func TestRepositoryZipvoiceVoices(t *testing.T) {
	reg := newRegistry(t.TempDir(), "voices", "", 1, []string{"zipvoice"})
	e := reg.engines["zipvoice"]
	entries, err := os.ReadDir(reg.voicesDir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".wav") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".wav")
		if samples, sr, text, err := e.resolveReference(name); err != nil || len(samples) == 0 || sr <= 0 || text == "" {
			t.Fatalf("仓库音色 %s 无法解析：sr=%d text=%q err=%v", name, sr, text, err)
		}
		want = append(want, "local:zipvoice:"+name)
	}
	if len(want) == 0 {
		t.Fatal("仓库应包含参考音色")
	}
	provider := localProvider{reg: reg}
	if voices, err := provider.Voices(context.Background()); err != nil || len(voices) != 0 {
		t.Fatalf("未就绪引擎不应列出音色：%v %v", voices, err)
	}
	e.ready = true
	dir := t.TempDir()
	gw, err := newGateway(gatewayOptions{
		registry: reg, configFile: filepath.Join(dir, "config.json"), cacheDir: filepath.Join(dir, "cache"),
		getenv: func(key string) string {
			if key == "TTS_DEFAULT_VOICE" {
				return "local:melo:0"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := call(gw.routes(), http.MethodGet, "/v1/voices", "", nil)
	var response tts.VoicesResponse
	if rec.Code != http.StatusOK {
		t.Fatalf("音色列表失败：%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, voice := range response.Voices {
		got = append(got, voice.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("应仅提供目录音色：got=%v want=%v", got, want)
	}
	if !response.Available || response.DefaultVoice != "" {
		t.Fatalf("目录音色应可选，已停用的 Melo 默认值应忽略：%+v", response)
	}
}

func TestNormalizeKeepsShortSpeech(t *testing.T) {
	cases := []struct{ in, want string }{
		{"“老牛，刨！”", "老牛，刨！"},
		{"哞！", "哞！"},
		{"一人一牛", "一人一牛"},
		{"他说：“哞！”然后继续。", "他说：哞！然后继续。"},
		{"“走走走……”，他说。", "走走走。他说。"},
		{"  （笑）  ", "笑"},
		{"red(blue)green", "red blue green"},
		{"1（2）3", "1 2 3"},
		{"red（blue(green)）white", "red blue green white"},
		{"one\ntwo\nthree", "one two three"},
		{"1\n2\n3", "1 2 3"},
		{"（）( )", ""},
	}
	for _, c := range cases {
		got := normalize(c.in)
		if got != c.want {
			t.Errorf("normalize(%q) = %q，期望 %q", c.in, got, c.want)
		}
		if again := normalize(got); again != got {
			t.Errorf("normalize 不幂等：%q → %q", got, again)
		}
	}
	// 汉字和重复字不能因为预处理被删除。
	for _, text := range []string{"一人一牛", "哞！", "“老牛，刨！”"} {
		before, after := countSpokenUnits(text), countSpokenUnits(normalize(text))
		if before != after {
			t.Errorf("%q 的有效字数被改动：%d → %d", text, before, after)
		}
	}
	if strings.HasPrefix(normalize("“老牛，刨！”"), "，") || strings.Contains(normalize("“老牛，刨！”"), "！，") {
		t.Errorf("引号不应制造多余逗号：%q", normalize("“老牛，刨！”"))
	}
}
