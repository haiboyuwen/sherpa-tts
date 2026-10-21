package main

import (
	"encoding/binary"
	"fmt"
	"math"
)

// decodeWAV 解析 16 位整型或 32 位浮点的 PCM WAV，返回归一化采样和采样率。
// 只认单/多声道的标准 RIFF 块；多声道按声道数平均混成单声道（ZipVoice 参考音频需要）。
func decodeWAV(data []byte) (samples []float32, sampleRate int, err error) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("不是 WAV 文件")
	}
	var channels, bits int
	var fmtTag uint16
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := data[pos+8:]
		if size < 0 || len(body) < size {
			return nil, 0, fmt.Errorf("WAV 块不完整")
		}
		switch id {
		case "fmt ":
			fmtTag = binary.LittleEndian.Uint16(body[0:2])
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			sampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			if channels == 0 {
				return nil, 0, fmt.Errorf("WAV 缺少 fmt 块")
			}
			switch {
			case fmtTag == 1 && bits == 16:
				n := size / 2 / channels
				samples = make([]float32, n)
				for i := 0; i < n; i++ {
					var sum float64
					for c := 0; c < channels; c++ {
						v := int16(binary.LittleEndian.Uint16(body[i*channels*2+c*2:]))
						sum += float64(v) / 32768
					}
					samples[i] = float32(sum / float64(channels))
				}
			case fmtTag == 3 && bits == 32, fmtTag == 0xFFFE && bits == 32:
				n := size / 4 / channels
				samples = make([]float32, n)
				for i := 0; i < n; i++ {
					var sum float64
					for c := 0; c < channels; c++ {
						sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(body[i*channels*4+c*4:])))
					}
					samples[i] = float32(sum / float64(channels))
				}
			default:
				return nil, 0, fmt.Errorf("不支持的 WAV 编码：fmt=%d bits=%d，请转成 16 位 PCM", fmtTag, bits)
			}
			return samples, sampleRate, nil
		}
		pos += 8 + size + size%2 // 块按 2 字节对齐
	}
	return nil, 0, fmt.Errorf("WAV 缺少 data 块")
}

// voicedSeconds 估算有人声的时长：按 20ms 分帧，RMS 高于峰值 -35dB 的帧算有声。
func voicedSeconds(samples []float32, sampleRate int) float64 {
	win := sampleRate / 50
	if win == 0 {
		return 0
	}
	var peak float32
	for _, v := range samples {
		peak = max(peak, float32(math.Abs(float64(v))))
	}
	if peak == 0 {
		return 0
	}
	threshold := float64(peak) * math.Pow(10, -35.0/20)
	voiced := 0
	for i := 0; i+win <= len(samples); i += win {
		var sum float64
		for _, v := range samples[i : i+win] {
			sum += float64(v) * float64(v)
		}
		if math.Sqrt(sum/float64(win)) > threshold {
			voiced++
		}
	}
	return float64(voiced*win) / float64(sampleRate)
}
