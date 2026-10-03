// Command sherpa-tts 是中文语音合成网关：内置本地模型（sherpa-onnx + Kokoro / MeloTTS，纯 CPU），
// 并可配置第三方服务（OpenAI 兼容、Azure、阿里云百炼、火山引擎豆包）。应用后端把朗读请求和
// 后台配置都转发到这里；配置接口由 sherpa-tts 提供，各项目自己实现配置页面。
//
// 接口见 README。模型首次启动时下载到 TTS_MODEL_DIR；配置与合成缓存在 TTS_DATA_DIR。
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "探测本机 /health 后退出（供 Docker HEALTHCHECK 使用）")
	flag.Parse()

	port := env("TTS_PORT", "8000")
	if *healthcheck {
		os.Exit(probe(port))
	}

	threads, _ := strconv.Atoi(env("TTS_THREADS", "4"))
	if threads <= 0 {
		threads = 4
	}
	// TTS_ENGINES 设为 none 可以只做第三方网关、不下载本地模型。
	reg := newRegistry(
		env("TTS_MODEL_DIR", "/models"),
		env("TTS_MODEL_MIRROR", "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models"),
		threads,
		strings.Split(env("TTS_ENGINES", "melo,kokoro"), ","),
	)
	reg.loadAll()

	dataDir := env("TTS_DATA_DIR", "/data")
	gw, err := newGateway(gatewayOptions{
		registry:   reg,
		configFile: env("TTS_CONFIG_FILE", filepath.Join(dataDir, "config.json")),
		cacheDir:   filepath.Join(dataDir, "cache"),
		apiToken:   os.Getenv("TTS_API_TOKEN"),
	})
	if err != nil {
		log.Printf("配置无效，第三方提供商暂不可用：%v", err)
	}

	srv := &http.Server{Addr: ":" + port, Handler: gw.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("sherpa-tts listening on :%s, engines=%v, providers=%d, token=%v",
		port, reg.names(), len(gw.svc.Config().Providers), gw.apiToken != "")
	log.Fatal(srv.ListenAndServe())
}

func probe(port string) int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
