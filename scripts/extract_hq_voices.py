#!/usr/bin/env python3
"""
从 B站纯人声视频提取高质量参考音频，用于 ZipVoice 克隆。
输出到 voices/ 目录：<名字>.wav + <名字>.txt
"""

import subprocess
import tempfile
from pathlib import Path

# 纯人声视频列表
VOICES = [
    {
        "name": "董卿",
        "url": "https://www.bilibili.com/video/BV1wsoBY8Ed6",
        "start": "00:10",
        "duration": 8,
        "text": "大家好，我是董卿。声音是灵魂的载体，让我们一起感受语言的力量。",
    },
    {
        "name": "再别康桥",
        "url": "https://www.bilibili.com/video/BV1wN41197gi",
        "start": "00:05",
        "duration": 8,
        "text": "轻轻的我走了，正如我轻轻的来。我轻轻的招手，作别西天的云彩。",
    },
    {
        "name": "雨巷",
        "url": "https://www.bilibili.com/video/BV1T5411D77X",
        "start": "00:10",
        "duration": 8,
        "text": "撑着油纸伞，独自彷徨在悠长、悠长又寂寥的雨巷，我希望逢着一个丁香一样的姑娘。",
    },
]


def download_and_extract(voice_info: dict, output_dir: Path, cookies_file: str = None):
    """下载视频并提取音频片段"""
    name = voice_info["name"]
    url = voice_info["url"]
    start = voice_info["start"]
    duration = voice_info["duration"]
    text = voice_info["text"]

    wav_path = output_dir / f"{name}.wav"
    txt_path = output_dir / f"{name}.txt"

    print(f"处理: {name}")

    with tempfile.TemporaryDirectory() as tmpdir:
        # 下载音频
        cmd = [
            "yt-dlp",
            "-x",
            "--audio-format", "wav",
            "--audio-quality", "0",
            "-o", f"{tmpdir}/audio.%(ext)s",
            "--no-playlist",
        ]
        if cookies_file:
            cmd.extend(["--cookies", cookies_file])
        cmd.append(url)

        print(f"  下载中: {url}")
        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"  ✗ 下载失败: {result.stderr[:100]}")
            return False

        # 找到下载的 wav 文件
        wav_files = list(Path(tmpdir).glob("audio*.wav"))
        if not wav_files:
            print("  ✗ 未找到音频文件")
            return False

        # 提取片段，转换为 16kHz 16-bit mono
        full_audio = wav_files[0]
        cmd = [
            "ffmpeg", "-y",
            "-i", str(full_audio),
            "-ss", start,
            "-t", str(duration),
            "-ar", "16000",
            "-ac", "1",
            "-sample_fmt", "s16",
            str(wav_path),
        ]
        subprocess.run(cmd, capture_output=True, check=True)

    # 写入文本
    txt_path.write_text(text, encoding="utf-8")

    print(f"  ✓ {wav_path}")
    print(f"  ✓ {txt_path}")
    return True


def main():
    output_dir = Path("voices")
    output_dir.mkdir(exist_ok=True)

    cookies_file = Path("/tmp/claude-501/bilibili_cookies.txt")
    if not cookies_file.exists():
        cookies_file = None

    print(f"开始提取 {len(VOICES)} 个高质量参考音频...")
    print(f"输出目录: {output_dir.absolute()}\n")

    success = 0
    for voice_info in VOICES:
        try:
            if download_and_extract(voice_info, output_dir, cookies_file):
                success += 1
        except Exception as e:
            print(f"  ✗ 失败: {e}")

    print(f"\n完成: {success}/{len(VOICES)} 成功")
    print(f"\n下一步:")
    print(f"  docker cp voices/. sherpa-tts-tts-1:/data/voices/")
    print(f"  scripts/audition.sh zipvoice")


if __name__ == "__main__":
    main()
