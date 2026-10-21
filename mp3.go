package main

/*
#cgo LDFLAGS: -lmp3lame
#cgo darwin CFLAGS: -I/opt/homebrew/include -I/usr/local/include
#cgo darwin LDFLAGS: -L/opt/homebrew/lib -L/usr/local/lib
#include <stdlib.h>
#include <lame/lame.h>
*/
import "C"

import (
	"errors"
	"math"
	"unsafe"
)

// encodeMP3 把单声道浮点采样编码为 64kbps CBR mp3（libmp3lame），超出满幅时整体缩放而不是削波。
func encodeMP3(samples []float32, sampleRate int) ([]byte, error) {
	if len(samples) == 0 {
		return nil, errors.New("no samples")
	}
	// 先整体缩放到峰值 0.95 再量化。ZipVoice 的输出峰值常到 1.5–3.5，
	// 逐样本截断会把波峰削平，听起来就是沙哑的杂音。
	gain := peakGain(samples, 0.95)
	pcm := make([]C.short, len(samples))
	for i, s := range samples {
		v := float64(s) * gain
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		pcm[i] = C.short(v * 32767)
	}

	gf := C.lame_init()
	if gf == nil {
		return nil, errors.New("lame_init failed")
	}
	defer C.lame_close(gf)
	C.lame_set_num_channels(gf, 1)
	C.lame_set_mode(gf, C.MONO)
	C.lame_set_in_samplerate(gf, C.int(sampleRate))
	C.lame_set_VBR(gf, C.vbr_off)
	C.lame_set_brate(gf, 64)
	C.lame_set_quality(gf, 2)
	if C.lame_init_params(gf) < 0 {
		return nil, errors.New("lame_init_params failed")
	}

	// lame 文档给出的最坏情况：1.25 * 采样数 + 7200。
	out := make([]byte, len(samples)*5/4+7200)
	n := C.lame_encode_buffer(gf, &pcm[0], &pcm[0], C.int(len(pcm)), (*C.uchar)(unsafe.Pointer(&out[0])), C.int(len(out)))
	if n < 0 {
		return nil, errors.New("lame_encode_buffer failed")
	}
	tail := make([]byte, 7200)
	m := C.lame_encode_flush(gf, (*C.uchar)(unsafe.Pointer(&tail[0])), C.int(len(tail)))
	if m < 0 {
		return nil, errors.New("lame_encode_flush failed")
	}
	return append(out[:n], tail[:m]...), nil
}

// peakGain 返回把峰值压到 target 所需的增益；峰值不超过 target 时不放大，避免抬升底噪。
func peakGain(samples []float32, target float64) float64 {
	var peak float64
	for _, s := range samples {
		if v := math.Abs(float64(s)); v > peak {
			peak = v
		}
	}
	if peak <= target {
		return 1
	}
	return target / peak
}
