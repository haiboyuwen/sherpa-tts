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
	"unsafe"
)

// encodeMP3 把 [-1, 1] 的单声道浮点采样编码为 64kbps CBR mp3（libmp3lame）。
func encodeMP3(samples []float32, sampleRate int) ([]byte, error) {
	if len(samples) == 0 {
		return nil, errors.New("no samples")
	}
	pcm := make([]C.short, len(samples))
	for i, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		pcm[i] = C.short(s * 32767)
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
