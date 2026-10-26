package main

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	zipvoiceChunkMax = 200
	zipvoiceChunkMin = 30
)

// splitZipvoiceText 保留所有字符，长请求在自然词界附近均分，避免 200+1 这样的短尾。
func splitZipvoiceText(text string) []string {
	chars := []rune(text)
	if len(chars) == 0 {
		return nil
	}
	remaining := (len(chars) + zipvoiceChunkMax - 1) / zipvoiceChunkMax
	chunks := make([]string, 0, remaining)
	start := 0
	for remaining > 1 {
		n := len(chars) - start
		target := n / remaining
		low := max(zipvoiceChunkMin, n-(remaining-1)*zipvoiceChunkMax)
		high := min(zipvoiceChunkMax, n-(remaining-1)*zipvoiceChunkMin)
		cut, distance := target, zipvoiceChunkMax+1
		for i := low; i <= high; i++ {
			left := chars[start+i-1]
			if !unicode.IsSpace(left) && !strings.ContainsRune("。！？!?；;，,、：:….", left) {
				continue
			}
			d := i - target
			if d < 0 {
				d = -d
			}
			if d < distance {
				cut, distance = i, d
			}
		}
		chunks = append(chunks, string(chars[start:start+cut]))
		start += cut
		remaining--
	}
	return append(chunks, string(chars[start:]))
}

// generateZipvoice 让每个有界块整体推理；Extra 防止原生前端再次留下短句尾段。
func generateZipvoice(text string, reference sherpa.GenerationConfig, generate func(string, *sherpa.GenerationConfig) *sherpa.GeneratedAudio) (*sherpa.GeneratedAudio, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errEmptyText
	}
	out := &sherpa.GeneratedAudio{}
	for _, chunk := range splitZipvoiceText(text) {
		extra, err := json.Marshal(struct {
			Min int `json:"min_char_in_sentence"`
			Max int `json:"max_char_in_sentence"`
		}{utf8.RuneCountInString(chunk) + 1, zipvoiceChunkMax})
		if err != nil {
			return nil, err
		}
		config := reference
		config.Extra = extra
		audio := generate(chunk, &config)
		if audio == nil || len(audio.Samples) == 0 {
			return nil, errors.New("no audio generated")
		}
		if audio.SampleRate <= 0 {
			return nil, errors.New("invalid audio sample rate")
		}
		if out.SampleRate == 0 {
			out.SampleRate = audio.SampleRate
		} else if out.SampleRate != audio.SampleRate {
			return nil, errors.New("inconsistent audio sample rates")
		}
		out.Samples = append(out.Samples, audio.Samples...)
	}
	return out, nil
}
