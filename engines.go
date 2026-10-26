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
	// extras 是归档之外的独立文件（如 vocoder），从同站点的其它 release 下载到解压目录。
	extras []extraFile
	// dynamicVoices = true 表示音色来自参考音频目录（zipvoice 克隆模型）。
	dynamicVoices bool
}

type extraFile struct {
	release string // GitHub release 名，如 vocoder-models
	file    string // 资产文件名
}

var specs = map[string]engineSpec{
	// zipvoice 是零样本克隆模型：没有固定音色表，音色 = voices 目录里的一对文件
	// <名字>.wav（参考音频）+ <名字>.txt（参考文字）。
	"zipvoice": {
		archive:       "sherpa-onnx-zipvoice-distill-int8-zh-en-emilia",
		label:         "ZipVoice 克隆",
		dynamicVoices: true,
		extras:        []extraFile{{release: "vocoder-models", file: "vocos_24khz.onnx"}},
		config: func(d string, threads int) *sherpa.OfflineTtsConfig {
			// 缩放参数取 ZipVoice 上游默认值；蒸馏模型 4 步即可。
			cfg := &sherpa.OfflineTtsConfig{}
			cfg.Model.Zipvoice = sherpa.OfflineTtsZipvoiceModelConfig{
				Tokens:        d + "/tokens.txt",
				Encoder:       d + "/encoder.int8.onnx",
				Decoder:       d + "/decoder.int8.onnx",
				DataDir:       d + "/espeak-ng-data",
				Lexicon:       d + "/lexicon.txt",
				Vocoder:       d + "/vocos_24khz.onnx",
				FeatScale:     0.1,
				TShift:        0.5,
				TargetRms:     0.1,
				GuidanceScale: 1.0,
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
	reg  *registry

	mu    sync.Mutex // 同一引擎串行推理
	tts   *sherpa.OfflineTts
	state sync.Mutex
	ready bool
	err   string
}

type registry struct {
	modelDir  string
	voicesDir string
	mirror    string
	threads   int
	engines   map[string]*engine
}

func newRegistry(modelDir, voicesDir, mirror string, threads int, enabled []string) *registry {
	r := &registry{modelDir: modelDir, voicesDir: voicesDir, mirror: strings.TrimRight(mirror, "/"), threads: threads, engines: map[string]*engine{}}
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		if spec, ok := specs[name]; ok {
			r.engines[name] = &engine{name: name, spec: spec, reg: r}
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
		out[name] = map[string]any{"label": e.spec.label, "ready": e.ready, "error": e.err, "voices": e.listVoices()}
		e.state.Unlock()
	}
	return out
}

func (e *engine) isReady() bool {
	e.state.Lock()
	defer e.state.Unlock()
	return e.ready
}

// listVoices 返回对外音色：静态表（melo）或参考音频目录扫描（zipvoice）。
func (e *engine) listVoices() []voiceInfo {
	if !e.spec.dynamicVoices {
		return e.spec.voices
	}
	entries, err := os.ReadDir(e.reg.voicesDir)
	if err != nil {
		return nil
	}
	var out []voiceInfo
	for _, f := range entries {
		name := f.Name()
		if !f.Type().IsRegular() || !strings.HasSuffix(name, ".wav") {
			continue
		}
		stem := strings.TrimSuffix(name, ".wav")
		if _, _, _, err := e.resolveReference(stem); err != nil {
			continue
		}
		out = append(out, voiceInfo{ID: 0, Label: stem})
	}
	// Label 即克隆音色名，与目录内的文件名一致。
	return out
}

// resolveReference 把音色名解析成参考音频采样、采样率和参考文字。
func (e *engine) resolveReference(voice string) ([]float32, int, string, error) {
	if voice == "" || strings.ContainsAny(voice, "/\\\x00") || strings.HasPrefix(voice, ".") {
		return nil, 0, "", errors.New("非法音色名")
	}
	dir := e.reg.voicesDir
	text, err := os.ReadFile(filepath.Join(dir, voice+".txt"))
	if err != nil {
		return nil, 0, "", errors.New("音色缺少参考文本 " + voice + ".txt")
	}
	wav, err := os.ReadFile(filepath.Join(dir, voice+".wav"))
	if err != nil {
		return nil, 0, "", errors.New("音色缺少参考音频 " + voice + ".wav")
	}
	samples, sr, err := decodeWAV(wav)
	if err != nil {
		return nil, 0, "", err
	}
	if len(samples) < sr {
		return nil, 0, "", errors.New("参考音频太短，至少 1 秒")
	}
	if len(samples) > sr*30 {
		return nil, 0, "", errors.New("参考音频太长，请剪到 30 秒内（建议 5–10 秒）")
	}
	refText := strings.TrimSpace(string(text))
	if refText == "" {
		return nil, 0, "", errors.New("参考文本不能为空")
	}
	// 参考文字必须正好是参考音频里读出的内容。文字比音频多时，ZipVoice 会把
	// 音频里没读到的那部分挪到生成结果开头念出来。正常语速约 5–7.5 字/秒，
	// 按有声时长算超过 maxRefCharsPerSec 基本就是文字和音频对不上。
	if n, vs := countSpokenUnits(refText), voicedSeconds(samples, sr); vs > 0 && float64(n)/vs > maxRefCharsPerSec {
		return nil, 0, "", fmt.Errorf("参考文本与参考音频不匹配：%s.wav 只有约 %.1f 秒人声，%s.txt 却有 %d 个字，请让文本与音频逐字对应", voice, vs, voice, n)
	}
	return samples, sr, refText, nil
}

const maxRefCharsPerSec = 9.0

var spokenUnit = regexp.MustCompile(`\p{Han}|[A-Za-z]+|\d`)

// countSpokenUnits 统计要念出的单位数：每个汉字、每个英文单词、每个数字各算一个。
func countSpokenUnits(text string) int {
	return len(spokenUnit.FindAllStringIndex(text, -1))
}

func (e *engine) synthesize(voice string, text string) ([]byte, error) {
	if !e.isReady() {
		return nil, errNotReady
	}
	text = normalize(text)
	if text == "" {
		return nil, errEmptyText
	}
	e.mu.Lock()
	var audio *sherpa.GeneratedAudio
	if e.spec.dynamicVoices {
		refSamples, refSR, refText, err := e.resolveReference(voice)
		if err != nil {
			e.mu.Unlock()
			return nil, err
		}
		audio, err = generateZipvoice(text, sherpa.GenerationConfig{
			Speed:               1.0,
			NumSteps:            4, // 蒸馏模型官方推荐步数
			ReferenceAudio:      refSamples,
			ReferenceSampleRate: refSR,
			ReferenceText:       refText,
		}, func(chunk string, config *sherpa.GenerationConfig) *sherpa.GeneratedAudio {
			return e.tts.GenerateWithConfig(chunk, config, nil)
		})
		if err != nil {
			e.mu.Unlock()
			return nil, err
		}
	} else {
		id, err := strconv.Atoi(voice)
		if err != nil {
			e.mu.Unlock()
			return nil, errors.New("未知音色 " + voice)
		}
		audio = e.tts.Generate(text, id, 1.0)
	}
	e.mu.Unlock()
	if audio == nil || len(audio.Samples) == 0 {
		return nil, errors.New("no audio generated")
	}
	return encodeMP3(audio.Samples, audio.SampleRate)
}

// 模型的词表不认弯引号、书名号等。引号去掉，避免制造首尾逗号；
// 括号保留为空格，防止 red(blue)green 或 1（2）3 粘连成另一个词或数值。
var punct = strings.NewReplacer(
	"“", "", "”", "", "‘", "", "’", "", "「", "", "」", "", "『", "", "』", "",
	"《", "", "》", "", "（", " ", "）", " ", "(", " ", ")", " ", "—", "，", "…", "。",
)
var (
	repeatedComma = regexp.MustCompile(`[，,]{2,}`)
	repeatedStop  = regexp.MustCompile(`[。]{2,}`)
	commaAfterEnd = regexp.MustCompile(`([。！？!?；;])[，,]+`)
	leadingComma  = regexp.MustCompile(`^[，,]+`)
	spaces        = regexp.MustCompile(`\s+`)
)

func normalize(text string) string {
	text = punct.Replace(text)
	text = repeatedComma.ReplaceAllString(text, "，")
	text = repeatedStop.ReplaceAllString(text, "。")
	text = commaAfterEnd.ReplaceAllString(text, "$1")
	text = strings.TrimSpace(spaces.ReplaceAllString(text, " "))
	return leadingComma.ReplaceAllString(text, "")
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
	for _, x := range e.spec.extras {
		if err := r.downloadExtra(dir, x); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// downloadExtra 下载归档外的独立文件（如 vocoder）。镜像源按 GitHub releases/download
// 目录结构镜像整棵树，所以把镜像 URL 最后一段（tts-models）换成目标 release 名即可。
func (r *registry) downloadExtra(dir string, x extraFile) error {
	dst := filepath.Join(dir, x.file)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	base := r.mirror[:strings.LastIndex(r.mirror, "/")]
	url := base + "/" + x.release + "/" + x.file
	log.Printf("[%s] downloading %s", "", url)
	client := http.Client{Timeout: 60 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o644)
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
		for _, v := range e.listVoices() {
			id := strconv.Itoa(v.ID)
			if e.spec.dynamicVoices {
				id = v.Label // zipvoice 音色 ID 是名字,如 zipvoice:旁白1
			}
			voices = append(voices, tts.VoiceOption{ID: name + ":" + id, Label: v.Label})
		}
	}
	return voices, nil
}

func (p localProvider) Synthesize(_ context.Context, voice, text string) (tts.Audio, error) {
	name, sid, _ := strings.Cut(voice, ":")
	e, ok := p.reg.engines[name]
	if !ok {
		return tts.Audio{}, &tts.ProviderError{Provider: "本地模型", Message: "未知音色 " + voice}
	}
	audio, err := e.synthesize(sid, text)
	if err != nil {
		return tts.Audio{}, &tts.ProviderError{Provider: "本地模型", Message: err.Error()}
	}
	return tts.Audio{Data: audio, ContentType: "audio/mpeg"}, nil
}
