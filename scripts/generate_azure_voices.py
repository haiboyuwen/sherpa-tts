#!/usr/bin/env python3
"""
通过 Edge TTS（免费）生成 Azure 神经网络声音的参考音频，用于 ZipVoice 克隆。
输出到 voices/ 目录：<名字>.wav + <名字>.txt

Edge TTS 使用和 Azure 相同的神经网络声音，无需 API Key。
"""

import asyncio
import subprocess
import edge_tts
from pathlib import Path

# 6 个目标音色
VOICES = [
    {
        "name": "晓晓",
        "voice": "zh-CN-XiaoxiaoNeural",
        "text": "大家好，我是晓晓，一个温柔自然的 AI 声音。很高兴能用声音陪伴你。",
    },
    {
        "name": "云希",
        "voice": "zh-CN-YunxiNeural",
        "text": "大家好，我是云希，一个充满活力的青年男声。让我们一起探索精彩的世界。",
    },
    {
        "name": "云健",
        "voice": "zh-CN-YunjianNeural",
        "text": "大家好，我是云健，一个成熟稳重的声音。适合解说和深度内容。",
    },
    {
        "name": "晓伊",
        "voice": "zh-CN-XiaoyiNeural",
        "text": "嗨，我是晓伊，一个活泼甜美的女声。希望能带给你愉快的体验。",
    },
    {
        "name": "云扬",
        "voice": "zh-CN-YunyangNeural",
        "text": "大家好，我是云扬，一个稳重专业的播音声。适合新闻和正式场合。",
    },
    {
        "name": "晓辰",
        "voice": "zh-CN-XiaochenNeural",
        "text": "大家好，我是晓辰，一个清亮悦耳的女声。期待用声音为你服务。",
    },
    {
        "name": "云夏",
        "voice": "zh-CN-YunxiaNeural",
        "text": "嗨，我是云夏，一个可爱的男声。让我们一起进入有趣的故事世界。",
    },
    {
        "name": "小北",
        "voice": "zh-CN-liaoning-XiaobeiNeural",
        "text": "哎呀妈呀，我是小北，东北那嘎达的。唠嗑贼有意思，整点啥都行。",
    },
    {
        "name": "小妮",
        "voice": "zh-CN-shaanxi-XiaoniNeural",
        "text": "额是小妮，陕西的。额这声音亮堂得很，给你说个事，美滴很。",
    },
]


async def generate_voice(voice_info: dict, output_dir: Path):
    """生成单个音色的参考音频"""
    name = voice_info["name"]
    voice = voice_info["voice"]
    text = voice_info["text"]

    wav_path = output_dir / f"{name}.wav"
    txt_path = output_dir / f"{name}.txt"
    mp3_path = output_dir / f"{name}.mp3"

    print(f"生成: {name} ({voice})")

    # 使用 Edge TTS 合成（输出 MP3）
    communicate = edge_tts.Communicate(text, voice)
    await communicate.save(str(mp3_path))

    # 转换为 16kHz 16-bit mono PCM WAV
    subprocess.run([
        "ffmpeg", "-y", "-i", str(mp3_path),
        "-ar", "16000", "-ac", "1", "-sample_fmt", "s16",
        str(wav_path)
    ], capture_output=True, check=True)
    mp3_path.unlink()

    # 写入文本
    txt_path.write_text(text, encoding="utf-8")

    print(f"  ✓ {wav_path}")
    print(f"  ✓ {txt_path}")


async def main():
    output_dir = Path("voices")
    output_dir.mkdir(exist_ok=True)

    print(f"开始生成 {len(VOICES)} 个 Azure 音色的参考音频...")
    print(f"输出目录: {output_dir.absolute()}\n")

    for voice_info in VOICES:
        try:
            await generate_voice(voice_info, output_dir)
        except Exception as e:
            print(f"  ✗ 失败: {e}")

    print(f"\n完成！")
    print(f"\n下一步:")
    print(f"  1. 将音频复制到 Docker 容器: docker cp voices/. <容器名>:/data/voices/")
    print(f"  2. 运行试听: scripts/audition.sh zipvoice")


if __name__ == "__main__":
    asyncio.run(main())
