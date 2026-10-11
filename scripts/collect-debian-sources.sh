#!/usr/bin/env bash
set -euo pipefail

app_image=${1:?usage: collect-debian-sources.sh APP_IMAGE OUTPUT_DIR}
output_dir=${2:?usage: collect-debian-sources.sh APP_IMAGE OUTPUT_DIR}

if [[ -e $output_dir ]]; then
  echo 'Debian source output directory already exists.' >&2
  exit 1
fi
mkdir -p "$output_dir/sources"

query_format='${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n'
docker run --rm --network none --entrypoint dpkg-query "$app_image" \
  -W -f="$query_format" > "$output_dir/app-packages.tsv"

python3 - "$output_dir" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
sources = set()
for line in (root / "app-packages.tsv").read_text().splitlines():
    parts = line.split("\t")
    if len(parts) != 4:
        raise SystemExit("Invalid app package inventory")
    binary, binary_version, source, source_version = parts
    if not binary or not binary_version or not source or not source_version:
        raise SystemExit(f"Missing source identity for {binary or 'app'}")
    if not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*", source):
        raise SystemExit(f"Unexpected Debian source name: {source}")
    if not re.fullmatch(r"[A-Za-z0-9.+:~_-]+", source_version):
        raise SystemExit(f"Unexpected Debian source version: {source_version}")
    sources.add((source, source_version))
(root / "source-packages.tsv").write_text(
    "".join(f"{name}\t{version}\n" for name, version in sorted(sources)), newline="\n"
)
if not sources:
    raise SystemExit("No Debian source packages found")
PY

absolute_output=$(cd "$output_dir" && pwd)
docker run --rm --network none --entrypoint cat "$app_image" \
  /etc/apt/sources.list.d/debian.sources > "$output_dir/app-apt-sources.sources"
python3 scripts/debian-snapshot-sources.py "$output_dir/app-apt-sources.sources" "$output_dir/snapshot.sources"
docker run --rm \
  --user 0:0 \
  --mount "type=bind,source=$absolute_output,target=/work" \
  --workdir /work \
  --entrypoint sh \
  "$app_image" \
  -ec '
    cat > /etc/apt/sources.list.d/folderhug-source.list <<EOF
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm main
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm-updates main
deb-src [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian-security bookworm-security main
EOF
    apt-get -o APT::Update::Error-Mode=any update -qq
    cd /work/sources
    : > /work/missing-sources.tsv
    while IFS="$(printf "\t")" read -r source version; do
      if ! apt-get source --download-only --only-source "$source=$version" > /work/apt-source.log 2>&1; then
        printf "%s\t%s\n" "$source" "$version" >> /work/missing-sources.tsv
      fi
    done < /work/source-packages.tsv
    if test -s /work/missing-sources.tsv; then
      # Historical metadata needs its expiry relaxed; signatures and hashes remain required.
      cp /work/snapshot.sources /etc/apt/sources.list.d/folderhug-snapshot.sources
      apt-get -o APT::Update::Error-Mode=any update -qq
      while IFS="$(printf "\t")" read -r source version; do
        if apt-get source --download-only --only-source "$source=$version" > /work/apt-source.log 2>&1; then
          continue
        fi
        echo "Exact Debian source unavailable: $source=$version" >&2
        tail -n 20 /work/apt-source.log >&2
        exit 1
      done < /work/missing-sources.tsv
    fi
  '
rm -f "$output_dir/apt-source.log" "$output_dir/missing-sources.tsv"

if ! find "$output_dir/sources" -maxdepth 1 -name '*.dsc' -print -quit | grep -q .; then
  echo 'No Debian source control files were downloaded.' >&2
  exit 1
fi
