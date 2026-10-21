package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildWAV(t *testing.T, fmtTag uint16, bits int, samples []int16) []byte {
	t.Helper()
	data := make([]byte, 0, len(samples)*bits/8)
	switch bits {
	case 16:
		for _, v := range samples {
			data = binary.LittleEndian.AppendUint16(data, uint16(v))
		}
	case 32:
		for _, v := range samples {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(float32(v)/32768))
		}
	}
	b := binary.LittleEndian.AppendUint16(nil, fmtTag)
	b = binary.LittleEndian.AppendUint16(b, 1) // 单声道
	b = binary.LittleEndian.AppendUint32(b, 16000)
	b = binary.LittleEndian.AppendUint32(b, uint32(16000*bits/8))
	b = binary.LittleEndian.AppendUint16(b, uint16(bits/8))
	b = binary.LittleEndian.AppendUint16(b, uint16(bits))

	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(data)))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = append(out, b...)
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

func TestDecodeWAV(t *testing.T) {
	src := []int16{0, 16384, -16384, 32767}
	got, sr, err := decodeWAV(buildWAV(t, 1, 16, src))
	if err != nil || sr != 16000 || len(got) != len(src) {
		t.Fatalf("16bit PCM: err=%v sr=%d n=%d", err, sr, len(got))
	}
	if got[1] < 0.49 || got[1] > 0.51 {
		t.Fatalf("16bit PCM 采样值错误: %v", got[1])
	}

	got, sr, err = decodeWAV(buildWAV(t, 3, 32, src))
	if err != nil || sr != 16000 || len(got) != len(src) {
		t.Fatalf("32bit float: err=%v sr=%d n=%d", err, sr, len(got))
	}

	if _, _, err := decodeWAV([]byte("not a wav")); err == nil {
		t.Fatal("非 WAV 应报错")
	}
}

func TestVoicedSeconds(t *testing.T) {
	// 1 秒正弦 + 2 秒静音：有声约 1 秒。
	samples := make([]float32, 3*16000)
	for i := 0; i < 16000; i++ {
		samples[i] = float32(0.5 * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	if got := voicedSeconds(samples, 16000); got < 0.95 || got > 1.05 {
		t.Fatalf("有声时长 = %.2f，期望约 1 秒", got)
	}
	if got := voicedSeconds(make([]float32, 16000), 16000); got != 0 {
		t.Fatalf("全静音应为 0，得到 %.2f", got)
	}
}

func TestResolveReferenceRejectsMismatchedText(t *testing.T) {
	dir := t.TempDir()
	// 2 秒人声 + 4 秒静音，配 26 个字：13 字/秒，远超正常语速。
	pcm := make([]int16, 6*16000)
	for i := 0; i < 2*16000; i++ {
		pcm[i] = int16(16000 * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	if err := os.WriteFile(filepath.Join(dir, "诗.wav"), buildWAV(t, 1, 16, pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "诗.txt"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &engine{spec: specs["zipvoice"], reg: &registry{voicesDir: dir}}

	write("轻轻的我走了，正如我轻轻的来。我轻轻的招手，作别西天的云彩。")
	if _, _, _, err := e.resolveReference("诗"); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("文本远多于音频应报不匹配，得到 %v", err)
	}

	write("再别康桥，徐志摩。")
	if _, _, text, err := e.resolveReference("诗"); err != nil || text != "再别康桥，徐志摩。" {
		t.Fatalf("匹配的文本不应报错: text=%q err=%v", text, err)
	}
}
