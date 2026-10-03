# sherpa-tts：本地中文 TTS（sherpa-onnx，CPU）。模型不进镜像：首次启动下载到 /models 卷。
FROM python:3.12-slim
RUN pip install --no-cache-dir sherpa-onnx==1.13.8 numpy==2.5.3 lameenc==1.8.4
WORKDIR /app
COPY server.py .
ENV TTS_MODEL_DIR=/models
VOLUME /models
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health', timeout=3)"
CMD ["python", "server.py"]
