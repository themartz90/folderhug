#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
source_sha=${2:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
app_image=${3:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
media_image=${4:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
output_dir=${5:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE MEDIA_IMAGE OUTPUT_DIR}
repository=${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY to the source repository}

if [[ ! $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]]; then
  echo 'Expected a version tag such as v1.2.3 or v1.2.3-rc.1.' >&2
  exit 1
fi
if [[ ! $source_sha =~ ^[a-f0-9]{40}$ ]]; then
  echo 'Expected the full source commit SHA.' >&2
  exit 1
fi
if [[ -e $output_dir ]]; then
  echo 'Release output directory already exists.' >&2
  exit 1
fi

app_repo=${app_image%:*}
media_repo=${media_image%:*}
app_digest=$(docker image inspect "$app_image" --format '{{range .RepoDigests}}{{println .}}{{end}}' | grep -F "$app_repo@sha256:" | head -n 1)
media_digest=$(docker image inspect "$media_image" --format '{{range .RepoDigests}}{{println .}}{{end}}' | grep -F "$media_repo@sha256:" | head -n 1)
if [[ -z $app_digest || -z $media_digest ]]; then
  echo 'Could not resolve both pulled images to immutable registry digests.' >&2
  exit 1
fi

mkdir -p "$output_dir/notices/docs" "$output_dir/notices/web/public/brand" \
  "$output_dir/notices/app-image/usr/share/doc" \
  "$output_dir/notices/media-image/usr/share/doc" \
  "$output_dir/notices/inventories"
cp compose.yaml "$output_dir/folderhug-compose-$tag.yaml"
cp compose.media.yaml "$output_dir/folderhug-media-$tag.yaml"
python3 - "$tag" "$output_dir" "$repository" <<'PY'
import pathlib
import sys

tag, output, repository = sys.argv[1:]
for name, image in (
    (f"folderhug-compose-{tag}.yaml", "folderhug"),
    (f"folderhug-media-{tag}.yaml", "folderhug-media"),
):
    path = pathlib.Path(output) / name
    suffix = "-media" if image == "folderhug-media" else ""
    old = f"ghcr.io/{repository.lower()}{suffix}:latest"
    new = old.removesuffix(":latest") + f":{tag}"
    content = path.read_text()
    if old not in content:
        raise SystemExit(f"Expected image default missing in {name}")
    path.write_text(content.replace(old, new))
PY
python3 - "$tag" "$source_sha" "$app_digest" "$media_digest" "$output_dir" <<'PY'
import json
import pathlib
import sys

tag, source_sha, app_digest, media_digest, output = sys.argv[1:]
manifest = {
    "schema_version": 1,
    "version_tag": tag,
    "source_sha": source_sha,
    "app_image": app_digest,
    "media_image": media_digest,
}
(pathlib.Path(output) / f"folderhug-images-{tag}.json").write_text(
    json.dumps(manifest, indent=2) + "\n"
)
PY

cp LICENSE "$output_dir/notices/LICENSE"
cp docs/THIRD_PARTY_NOTICES.md docs/THIRD_PARTY_LICENSES.txt \
  docs/MEDIA_WORKER.md "$output_dir/notices/docs/"
cp web/public/brand/FONT-LICENSE.txt "$output_dir/notices/web/public/brand/"
docker run --rm --network none --entrypoint dpkg-query "$app_image" \
  -W > "$output_dir/notices/inventories/app-debian-packages.txt"
docker run --rm --network none --entrypoint dpkg-query "$media_image" \
  -W > "$output_dir/notices/inventories/media-debian-packages.txt"
docker run --rm --network none --entrypoint cat "$media_image" \
  /usr/share/http-wrapper/ffmpeg-build.txt > "$output_dir/notices/inventories/ffmpeg-build.txt"
docker run --rm --network none --entrypoint tar "$app_image" \
  -C /usr/share/doc -cf - . | tar -C "$output_dir/notices/app-image/usr/share/doc" -xf -
docker run --rm --network none --entrypoint tar "$media_image" \
  -C /usr/share/doc -cf - . | tar -C "$output_dir/notices/media-image/usr/share/doc" -xf -
printf 'Source commit: %s\nRepository source: https://github.com/%s/archive/%s.tar.gz\nApp image: %s\nMedia image: %s\n' \
  "$source_sha" "$repository" "$source_sha" "$app_image" "$media_image" > "$output_dir/notices/build-source.txt"
tar -C "$output_dir" -czf "$output_dir/folderhug-notices-$tag.tar.gz" notices
rm -rf -- "$output_dir/notices"

bash scripts/collect-debian-sources.sh "$app_image" "$media_image" "$output_dir/debian-source"
tar -C "$output_dir" -czf "$output_dir/folderhug-debian-sources-$tag.tar.gz" debian-source
rm -rf -- "$output_dir/debian-source"
size=$(stat -c %s "$output_dir/folderhug-debian-sources-$tag.tar.gz")
if ((size > 1900000000)); then
  echo 'Debian source archive exceeds the supported release asset size; split and review it before drafting.' >&2
  exit 1
fi
(
  cd "$output_dir"
  sha256sum "folderhug-compose-$tag.yaml" "folderhug-media-$tag.yaml" \
    "folderhug-images-$tag.json" \
    "folderhug-notices-$tag.tar.gz" "folderhug-debian-sources-$tag.tar.gz" \
    > "folderhug-checksums-$tag.txt"
  sha256sum -c "folderhug-checksums-$tag.txt"
)
