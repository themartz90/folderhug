#!/usr/bin/env bash
set -euo pipefail

app_image=${1:?usage: collect-debian-sources.sh APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
media_image=${2:?usage: collect-debian-sources.sh APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
output_dir=${3:?usage: collect-debian-sources.sh APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}

if [[ -e $output_dir ]]; then
  echo 'Debian source output directory already exists.' >&2
  exit 1
fi
mkdir -p "$output_dir/sources"

query_format='${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n'
docker run --rm --network none --entrypoint dpkg-query "$app_image" \
  -W -f="$query_format" > "$output_dir/app-packages.tsv"
docker run --rm --network none --entrypoint dpkg-query "$media_image" \
  -W -f="$query_format" > "$output_dir/media-packages.tsv"

python3 - "$output_dir" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
sources = set()
for image in ("app", "media"):
    for line in (root / f"{image}-packages.tsv").read_text().splitlines():
        parts = line.split("\t")
        if len(parts) != 4:
            raise SystemExit(f"Invalid package inventory for {image}")
        binary, binary_version, source, source_version = parts
        if not binary or not binary_version or not source or not source_version:
            raise SystemExit(f"Missing source identity for {binary or image}")
        if not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*", source):
            raise SystemExit(f"Unexpected Debian source name: {source}")
        if not re.fullmatch(r"[A-Za-z0-9.+:~_-]+", source_version):
            raise SystemExit(f"Unexpected Debian source version: {source_version}")
        sources.add((source, source_version))
(root / "source-packages.tsv").write_text(
    "".join(f"{name}\t{version}\n" for name, version in sorted(sources))
)
if not sources:
    raise SystemExit("No Debian source packages found")
PY

absolute_output=$(cd "$output_dir" && pwd)
docker run --rm \
  --mount "type=bind,source=$absolute_output,target=/work" \
  --workdir /work \
  --entrypoint sh \
  debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 \
  -ec '
    cat > /etc/apt/sources.list.d/folderhug-source.list <<EOF
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm main
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm-updates main
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian-security bookworm-security main
EOF
    apt-get update -qq
    cd /work/sources
    while IFS="$(printf "\t")" read -r source version; do
      if ! apt-get source --download-only --only-source "$source=$version" > /work/apt-source.log 2>&1; then
        echo "Exact Debian source unavailable: $source=$version" >&2
        tail -n 20 /work/apt-source.log >&2
        exit 1
      fi
    done < /work/source-packages.tsv
  '
rm -f "$output_dir/apt-source.log"

if ! find "$output_dir/sources" -maxdepth 1 -name '*.dsc' -print -quit | grep -q .; then
  echo 'No Debian source control files were downloaded.' >&2
  exit 1
fi
