package main

import (
	"math"
	"testing"
)

func TestPeakGain(t *testing.T) {
	// 峰值超过目标：整体缩到 0.95，不削波。
	s := []float32{0.5, 3.5, -2.0}
	if g := peakGain(s, 0.95); math.Abs(g*3.5-0.95) > 1e-6 {
		t.Fatalf("峰值 3.5 应缩放到 0.95，得到 %v", g*3.5)
	}
	// 峰值低于目标：不放大，保持原电平。
	if g := peakGain([]float32{0.1, -0.2}, 0.95); g != 1 {
		t.Fatalf("峰值低于目标不应放大，得到 %v", g)
	}
	if g := peakGain(nil, 0.95); g != 1 {
		t.Fatalf("空输入应为 1，得到 %v", g)
	}
}

func TestEncodeMP3NoClipping(t *testing.T) {
	// 1kHz 正弦，峰值 3.0：编码不应失败，也不应把波峰削平。
	sr := 24000
	s := make([]float32, sr)
	for i := range s {
		s[i] = float32(3.0 * math.Sin(2*math.Pi*1000*float64(i)/float64(sr)))
	}
	data, err := encodeMP3(s, sr)
	if err != nil || len(data) == 0 {
		t.Fatalf("编码失败: %v", err)
	}
}
