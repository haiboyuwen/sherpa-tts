# sherpa-tts：中文语音合成网关（Go）。内置本地模型（sherpa-onnx，CPU），可配置第三方 TTS。
# 模型不进镜像：首次启动下载到 /models 卷；配置与合成缓存在 /data 卷。
FROM golang:1.26-bookworm AS build
RUN apt-get update \
  && apt-get install -y --no-install-recommends libmp3lame-dev \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal/ ./internal/
# sherpa-onnx-go-linux 自带预编译的 onnxruntime / sherpa-onnx 动态库，按架构拷出来放进运行镜像。
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/sherpa-tts . \
  && case "$(uname -m)" in \
       x86_64) triple=x86_64-unknown-linux-gnu ;; \
       aarch64) triple=aarch64-unknown-linux-gnu ;; \
       *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
     esac \
  && mkdir -p /out/lib \
  && cp "$(go env GOMODCACHE)"/github.com/k2-fsa/sherpa-onnx-go-linux@*/lib/"$triple"/*.so /out/lib/

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends libmp3lame0 ca-certificates tzdata \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lib/ /usr/local/lib/
RUN ldconfig
COPY --from=build /out/sherpa-tts /usr/local/bin/sherpa-tts
ENV TTS_MODEL_DIR=/models TTS_DATA_DIR=/data
VOLUME ["/models", "/data"]
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["sherpa-tts", "-healthcheck"]
ENTRYPOINT ["sherpa-tts"]
