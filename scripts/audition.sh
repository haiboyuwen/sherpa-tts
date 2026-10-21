#!/usr/bin/env bash
# 批量生成本地/云端音色的试听文件：scripts/audition.sh [过滤关键字，如 zipvoice 或 melo] [试听文字]
# 环境变量：TTS_URL（默认 http://127.0.0.1:8000）、TTS_API_TOKEN、OUT（默认 ./audition）
# 已生成过的文件自动跳过，可反复跑；结束后写 audition/index.tsv（音色ID/标签/文件名）便于对照筛选。
# 生成后在 Finder 里点开听，或 `afplay audition/local_zipvoice_leijun.mp3`。
set -euo pipefail

url=${TTS_URL:-http://127.0.0.1:8000}
out=${OUT:-audition}
filter=${1:-zipvoice}
text=${2:-夜深了，窗外下着小雨。他放下手中的书，轻声说道：明天的事，明天再说吧。}
token=${TTS_API_TOKEN:-}

# macOS 自带 bash 3.2 在 set -u 下展开空数组会报错，所以不用数组放鉴权头。
api() { curl -fsS --connect-timeout 5 --max-time 120 ${token:+-H "Authorization: Bearer $token"} "$@"; }

mkdir -p "$out"
voices=$(api "$url/v1/voices") || { echo "连不上 $url/v1/voices（服务没起，或需要设置 TTS_API_TOKEN）" >&2; exit 1; }

list=$(mktemp)
trap 'rm -f "$list" "$list.f"' EXIT
echo "$voices" | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not d.get("available", True):
    sys.exit("网关报告音色不可用：本地引擎还没 ready，或第三方提供商未配置")
for v in d["voices"]:
    print(v["id"] + "\t" + v.get("label", ""))
' > "$list"
grep -- "$filter" "$list" > "$list.f" || { echo "没有匹配 \"$filter\" 的音色" >&2; exit 1; }

total=$(wc -l < "$list.f" | tr -d ' ')
echo "匹配 $total 个音色，输出目录 $out"

ok=0; skip=0; fail=0
: > "$out/index.tsv"
while IFS=$'\t' read -r id label; do
  base="$out/${id//[: \/]/_}"
  existing=$(ls "$base".* 2>/dev/null | head -n 1 || true)
  if [ -n "$existing" ]; then
    skip=$((skip + 1))
    printf '%s\t%s\t%s\n' "$id" "$label" "$existing" >> "$out/index.tsv"
    continue
  fi
  body=$(python3 -c 'import json,sys; print(json.dumps({"voice":sys.argv[1],"text":sys.argv[2]}))' "$id" "$text")
  # 本地和多数云端返回 mp3，Qwen-TTS 返回 wav，按 Content-Type 决定扩展名。
  if ctype=$(api -d "$body" "$url/v1/synthesize" -o "$base.tmp" -w '%{content_type}'); then
    case "$ctype" in *wav*) ext=wav ;; *) ext=mp3 ;; esac
    mv "$base.tmp" "$base.$ext"
    ok=$((ok + 1))
    printf '%s\t%s\t%s.%s\n' "$id" "$label" "$base" "$ext" >> "$out/index.tsv"
    echo "ok   $id"
  else
    fail=$((fail + 1))
    rm -f "$base.tmp"
    echo "ERR  $id"
  fi
done < "$list.f"

echo "完成：新生成 ${ok}，跳过已有 ${skip}，失败 ${fail}（对照清单 $out/index.tsv）"
[ "$fail" -eq 0 ]
