package main

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/haiboyuwen/sherpa-tts/internal/tts"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

type voiceInfo struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// engineSpec 描述一个引擎：模型归档名、对外音色和怎么从解压目录组装 sherpa 配置。
type engineSpec struct {
	archive string
	label   string
	voices  []voiceInfo
	config  func(dir string, threads int) *sherpa.OfflineTtsConfig
}

var specs = map[string]engineSpec{
	"kokoro": {
		archive: "kokoro-multi-lang-v1_1",
		label:   "Kokoro",
		voices:  []voiceInfo{{ID: 3, Label: "Kokoro 中文 3"}},
		config: func(d string, threads int) *sherpa.OfflineTtsConfig {
			cfg := &sherpa.OfflineTtsConfig{RuleFsts: d + "/date-zh.fst," + d + "/number-zh.fst," + d + "/phone-zh.fst", MaxNumSentences: 1}
			cfg.Model.Kokoro = sherpa.OfflineTtsKokoroModelConfig{
				Model:       d + "/model.onnx",
				Voices:      d + "/voices.bin",
				Tokens:      d + "/tokens.txt",
				DataDir:     d + "/espeak-ng-data",
				DictDir:     d + "/dict",
				Lexicon:     d + "/lexicon-zh.txt," + d + "/lexicon-us-en.txt",
				Lang:        "zh",
				LengthScale: 1.0,
			}
			cfg.Model.NumThreads = threads
			cfg.Model.Provider = "cpu"
			return cfg
		},
	},
	"melo": {
		archive: "vits-melo-tts-zh_en",
		label:   "MeloTTS",
		voices:  []voiceInfo{{ID: 0, Label: "MeloTTS 中文"}},
		config: func(d string, threads int) *sherpa.OfflineTtsConfig {
			cfg := &sherpa.OfflineTtsConfig{RuleFsts: d + "/date.fst," + d + "/number.fst," + d + "/phone.fst", MaxNumSentences: 1}
			cfg.Model.Vits = sherpa.OfflineTtsVitsModelConfig{
				Model:       d + "/model.onnx",
				Lexicon:     d + "/lexicon.txt",
				Tokens:      d + "/tokens.txt",
				DictDir:     d + "/dict",
				NoiseScale:  0.667,
				NoiseScaleW: 0.8,
				LengthScale: 1.0,
			}
			cfg.Model.NumThreads = threads
			cfg.Model.Provider = "cpu"
			return cfg
		},
	},
}

var (
	errNotReady  = errors.New("engine not ready")
	errEmptyText = errors.New("empty text")
)

type engine struct {
	name string
	spec engineSpec

	mu    sync.Mutex // 同一引擎串行推理
	tts   *sherpa.OfflineTts
	state sync.Mutex
	ready bool
	err   string
}

type registry struct {
	modelDir string
	mirror   string
	threads  int
	engines  map[string]*engine
}

func newRegistry(modelDir, mirror string, threads int, enabled []string) *registry {
	r := &registry{modelDir: modelDir, mirror: strings.TrimRight(mirror, "/"), threads: threads, engines: map[string]*engine{}}
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if spec, ok := specs[name]; ok {
			r.engines[name] = &engine{name: name, spec: spec}
		}
	}
	return r
}

func (r *registry) names() []string {
	out := make([]string, 0, len(r.engines))
	for name := range r.engines {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// loadAll 后台并行下载并加载各引擎；单个失败不影响其它。
func (r *registry) loadAll() {
	for _, e := range r.engines {
		go func(e *engine) {
			dir, err := r.download(e)
			if err == nil {
				e.tts = sherpa.NewOfflineTts(e.spec.config(dir, r.threads))
				if e.tts == nil {
					err = errors.New("failed to create OfflineTts")
				}
			}
			e.state.Lock()
			defer e.state.Unlock()
			if err != nil {
				e.err = err.Error()
				log.Printf("[%s] failed: %v", e.name, err)
				return
			}
			e.ready = true
			log.Printf("[%s] ready", e.name)
		}(e)
	}
}

func (r *registry) health() map[string]any {
	out := map[string]any{}
	for name, e := range r.engines {
		e.state.Lock()
		out[name] = map[string]any{"label": e.spec.label, "ready": e.ready, "error": e.err, "voices": e.spec.voices}
		e.state.Unlock()
	}
	return out
}

func (e *engine) isReady() bool {
	e.state.Lock()
	defer e.state.Unlock()
	return e.ready
}

func (e *engine) synthesize(voice int, text string) ([]byte, error) {
	if !e.isReady() {
		return nil, errNotReady
	}
	text = normalize(text)
	if text == "" {
		return nil, errEmptyText
	}
	e.mu.Lock()
	audio := e.tts.Generate(text, voice, 1.0)
	e.mu.Unlock()
	if audio == nil || len(audio.Samples) == 0 {
		return nil, errors.New("no audio generated")
	}
	return encodeMP3(audio.Samples, audio.SampleRate)
}

// 模型的词表不认弯引号、书名号等，统一压成普通标点。
var punct = strings.NewReplacer(
	"“", "，", "”", "，", "‘", "，", "’", "，", "「", "，", "」", "，", "『", "，", "』", "，",
	"《", "", "》", "", "（", "，", "）", "，", "(", "，", ")", "，", "—", "，", "…", "。",
)
var (
	repeatedComma = regexp.MustCompile(`[，,]{2,}`)
	spaces        = regexp.MustCompile(`\s+`)
)

func normalize(text string) string {
	text = punct.Replace(text)
	text = repeatedComma.ReplaceAllString(text, "，")
	return strings.TrimSpace(spaces.ReplaceAllString(text, " "))
}

// download 确保模型解压在 modelDir/<archive>，完成后写 .complete 标记。
func (r *registry) download(e *engine) (string, error) {
	dir := filepath.Join(r.modelDir, e.spec.archive)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(r.modelDir, 0o755); err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/%s.tar.bz2", r.mirror, e.spec.archive)
	log.Printf("[%s] downloading %s", e.name, url)
	client := http.Client{Timeout: 60 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if err := untar(tar.NewReader(bzip2.NewReader(resp.Body)), r.modelDir); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// untar 解到 dest；拒绝越界路径和链接。
func untar(tr *tar.Reader, dest string) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, hdr.Name)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

// localProvider 把本地引擎包装成网关的内置提供商：音色 ID 为「引擎:编号」，如 melo:0。
type localProvider struct{ reg *registry }

func (p localProvider) Voices(context.Context) ([]tts.VoiceOption, error) {
	var voices []tts.VoiceOption
	for _, name := range p.reg.names() {
		e := p.reg.engines[name]
		if !e.isReady() {
			continue
		}
		for _, v := range e.spec.voices {
			voices = append(voices, tts.VoiceOption{ID: name + ":" + strconv.Itoa(v.ID), Label: v.Label})
		}
	}
	return voices, nil
}

func (p localProvider) Synthesize(_ context.Context, voice, text string) (tts.Audio, error) {
	name, sid, _ := strings.Cut(voice, ":")
	id, err := strconv.Atoi(sid)
	e, ok := p.reg.engines[name]
	if err != nil || !ok {
		return tts.Audio{}, &tts.ProviderError{Provider: "本地模型", Message: "未知音色 " + voice}
	}
	audio, err := e.synthesize(id, text)
	if err != nil {
		return tts.Audio{}, &tts.ProviderError{Provider: "本地模型", Message: err.Error()}
	}
	return tts.Audio{Data: audio, ContentType: "audio/mpeg"}, nil
}
