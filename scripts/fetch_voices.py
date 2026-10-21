#!/usr/bin/env python3
"""
从 B站 下载知名声优/网红/歌手的音频，生成 ZipVoice 克隆用的参考音频+文本。
输出到 voices/ 目录：<名字>.wav + <名字>.txt

依赖：
  pip3 install yt-dlp
  brew install ffmpeg  # 或 apt install ffmpeg

用法：
  python3 scripts/fetch_voices.py                  # 下载全部
  python3 scripts/fetch_voices.py --names 边江,李佳琦  # 只下载指定人物
  python3 scripts/foices.py --list                  # 列出可用人物
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

# 推荐人物列表：声优、网红、歌手
# 每个条目：名字、B站视频URL、时间段（可选）、参考文本（可选）
VOICES = [
    # 小说声优
    {
        "name": "头陀渊",
        "url": "https://www.bilibili.com/video/BV1Vs346cEn9",
        "desc": "声线多变，人物区分度高，情绪感染力强",
        "start": "00:30",
        "duration": 8,
        "text": "大家好，我是头陀渊，代表作《大奉打更人》《一念永恒》。",
    },
    {
        "name": "有声的紫襟",
        "url": "https://www.bilibili.com/video/BV1eDpw6xED2",
        "desc": "男中低音，磁性强，悬疑氛围出色",
        "start": "00:30",
        "duration": 8,
        "text": "大家好，我是有声的紫襟，代表作《阴间神探》《摸金天师》。",
    },
    {
        "name": "周建龙",
        "url": "https://www.bilibili.com/video/BV1r54y1f7vZ",
        "desc": "浑厚、有沧桑感，代入感强",
        "start": "00:30",
        "duration": 8,
        "text": "大家好，我是周建龙，代表作《鬼吹灯》《盗墓笔记》。",
    },
    {
        "name": "艾宝良",
        "url": "https://www.bilibili.com/video/BV1Bz421i7gy",
        "desc": "戏剧表现力强，人物塑造细腻",
        "start": "00:30",
        "duration": 8,
        "text": "大家好，我是艾宝良，代表作《鬼吹灯》《四世同堂》。",
    },
    {
        "name": "李野默",
        "url": "https://www.bilibili.com/video/BV1Fjej6KEyi",
        "desc": "沉稳厚重，传统文学叙事感强",
        "start": "00:30",
        "duration": 8,
        "text": "大家好，我是李野默，代表作《平凡的世界》。",
    },
    # 网红
    {
        "name": "李佳琦",
        "url": "https://www.bilibili.com/video/BV1wSht6aEw4",
        "desc": "直播带货一哥，标志性口头禅'买它'",
        "start": "00:15",
        "duration": 8,
        "text": "所有女生，这款真的超级好用，买它买它买它！",
    },
    {
        "name": "董宇辉",
        "url": "https://www.bilibili.com/video/BV1E34y1w7rY",
        "desc": "东方甄选主播，知识型直播带货代表",
        "start": "00:20",
        "duration": 8,
        "text": "当你背单词的时候，阿拉斯加的鳕鱼正跃出水面，让我们一起感受知识的力量。",
    },
    {
        "name": "罗翔",
        "url": "https://www.bilibili.com/video/BV1DM4y1E79p",
        "desc": "中国政法大学教授，B站法律科普顶流",
        "start": "00:25",
        "duration": 8,
        "text": "同学们好，我是罗翔，今天我们来聊一个有趣的法律案例。",
    },
]


def check_deps():
    """检查依赖工具"""
    missing = []
    for cmd in ["yt-dlp", "ffmpeg"]:
        if subprocess.run(["which", cmd], capture_output=True).returncode != 0:
            missing.append(cmd)
    if missing:
        print(f"缺少依赖：{', '.join(missing)}")
        print("安装方法：")
        print("  pip3 install yt-dlp")
        print("  brew install ffmpeg  # macOS")
        print("  apt install ffmpeg    # Linux")
        sys.exit(1)


def download_audio(url: str, output_path: Path, cookies_file: str = None) -> bool:
    """从 B站 下载音频"""
    with tempfile.TemporaryDirectory() as tmpdir:
        cmd = [
            "yt-dlp",
            "-x",  # 只提取音频
            "--audio-format", "wav",
            "--audio-quality", "0",  # 最佳质量
            "-o", str(Path(tmpdir) / "audio.%(ext)s"),
            "--no-playlist",
        ]
        if cookies_file:
            cmd.extend(["--cookies", cookies_file])
        cmd.append(url)
        print(f"  下载中: {url}")
        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"  下载失败: {result.stderr}")
            return False

        # 找到下载的 wav 文件
        wav_files = list(Path(tmpdir).glob("audio*.wav"))
        if not wav_files:
            print("  未找到音频文件")
            return False

        # 移动到目标路径
        wav_files[0].rename(output_path)
        print(f"  已下载: {output_path}")
        return True


def extract_segment(input_path: Path, output_path: Path, start: str, duration: int):
    """提取音频片段，转换为 16kHz 单声道"""
    cmd = [
        "ffmpeg",
        "-y",
        "-i", str(input_path),
        "-ss", start,
        "-t", str(duration),
        "-ar", "16000",  # 16kHz
        "-ac", "1",  # 单声道
        "-acodec", "pcm_s16le",  # 16-bit PCM
        str(output_path),
    ]
    subprocess.run(cmd, capture_output=True, check=True)
    print(f"  已裁剪: {start} 起 {duration}秒 -> {output_path}")


def process_voice(voice: dict, voices_dir: Path, cookies_file: str = None):
    """处理单个人物"""
    name = voice["name"]
    url = voice["url"]
    start = voice.get("start", "00:30")
    duration = voice.get("duration", 8)
    text = voice.get("text", "")

    print(f"\n处理: {name} ({voice.get('desc', '')})")

    wav_path = voices_dir / f"{name}.wav"
    txt_path = voices_dir / f"{name}.txt"

    # 下载完整音频到临时文件
    with tempfile.TemporaryDirectory() as tmpdir:
        full_audio = Path(tmpdir) / "full.wav"
        if not download_audio(url, full_audio, cookies_file):
            return False

        # 提取片段
        extract_segment(full_audio, wav_path, start, duration)

    # 写入文本
    if text:
        txt_path.write_text(text, encoding="utf-8")
        print(f"  已写入文本: {txt_path}")
    else:
        print(f"  警告: 缺少参考文本，请手动创建 {txt_path}")

    return True


def list_voices():
    """列出所有可用人物"""
    print("可用人物：\n")
    categories = {"声优": [], "网红": [], "歌手": []}

    # 简单分类
    for v in VOICES:
        desc = v.get("desc", "")
        if "配音" in desc or "声优" in desc:
            categories["声优"].append(v)
        elif "歌手" in desc or "音乐" in desc:
            categories["歌手"].append(v)
        else:
            categories["网红"].append(v)

    for cat, voices in categories.items():
        if voices:
            print(f"【{cat}】")
            for v in voices:
                print(f"  {v['name']:8s} {v.get('desc', '')}")
            print()


def main():
    parser = argparse.ArgumentParser(description="从 B站 下载知名人物音频用于 ZipVoice 克隆")
    parser.add_argument("--list", action="store_true", help="列出所有可用人物")
    parser.add_argument("--names", type=str, help="只下载指定人物，逗号分隔")
    parser.add_argument("--output", type=str, default="voices", help="输出目录 (默认: voices)")
    parser.add_argument("--cookies", type=str, help="B站 cookie 文件路径（Netscape 格式）")
    args = parser.parse_args()

    if args.list:
        list_voices()
        return

    check_deps()

    voices_dir = Path(args.output)
    voices_dir.mkdir(exist_ok=True)

    # 筛选要下载的人物
    targets = VOICES
    if args.names:
        names = [n.strip() for n in args.names.split(",")]
        targets = [v for v in VOICES if v["name"] in names]
        if not targets:
            print(f"未找到指定人物: {args.names}")
            sys.exit(1)

    print(f"开始下载 {len(targets)} 个人物的音频...")
    print(f"输出目录: {voices_dir.absolute()}\n")

    success = 0
    for voice in targets:
        try:
            if process_voice(voice, voices_dir, args.cookies):
                success += 1
        except Exception as e:
            print(f"  处理失败: {e}")

    print(f"\n完成: {success}/{len(targets)} 成功")
    print(f"\n下一步:")
    print(f"  1. 检查 voices/ 目录下的 .wav 和 .txt 文件")
    print(f"  2. 试听音频质量，必要时调整 start/duration 参数")
    print(f"  3. 重启 sherpa-tts 服务，新音色会自动出现在音色列表")


if __name__ == "__main__":
    main()
