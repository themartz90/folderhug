#!/bin/sh
set -eu

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo 'Usage: generate-media-fixtures.sh EMPTY_OUTPUT_DIRECTORY' >&2
  exit 2
fi

output=$1
mkdir -p "$output"
if [ -n "$(find "$output" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  echo 'The output directory must be empty.' >&2
  exit 2
fi

ffmpeg -hide_banner -nostdin -loglevel error -n \
  -f lavfi -i 'testsrc2=size=320x180:rate=5' -t 2 -an \
  -c:v mpeg4 -threads 1 -movflags +faststart "$output/faststart.mp4"
ffmpeg -hide_banner -nostdin -loglevel error -n \
  -f lavfi -i 'testsrc2=size=320x180:rate=5' -t 2 -an \
  -c:v mpeg4 -threads 1 "$output/moov-at-end.mp4"
ffmpeg -hide_banner -nostdin -loglevel error -n \
  -f lavfi -i 'testsrc2=size=320x180:rate=5' -t 2 -an \
  -c:v mpeg4 -threads 1 "$output/sample.mkv"

# A single raw frame exceeds the worker's input and allocation budgets.
ffmpeg -hide_banner -nostdin -loglevel error -n \
  -f lavfi -i 'color=c=blue:size=4096x4096:rate=1' -frames:v 1 -an \
  -c:v rawvideo -pix_fmt bgr24 -threads 1 "$output/over-budget.avi"
printf '\000\000\000\030ftypisom\000\000\000\000isom' > "$output/corrupt.mp4"
printf '#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nhttp://127.0.0.1:9/private.ts\n#EXT-X-ENDLIST\n' > "$output/playlist.m3u8"
printf 'Generated synthetic media fixtures in %s\n' "$output"
