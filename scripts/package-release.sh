#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE OUTPUT_DIR}
source_sha=${2:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE OUTPUT_DIR}
app_image=${3:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE OUTPUT_DIR}
output_dir=${4:?usage: package-release.sh VERSION_TAG SOURCE_SHA APP_IMAGE OUTPUT_DIR}

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

mkdir -p "$output_dir/notices"
cp compose.images.yaml "$output_dir/folderhug-compose-$tag.yaml"
cp LICENSE docs/THIRD_PARTY_NOTICES.md docs/THIRD_PARTY_LICENSES.txt \
  web/public/brand/FONT-LICENSE.txt "$output_dir/notices/"
docker run --rm --network none --entrypoint dpkg-query "$app_image" \
  -W > "$output_dir/notices/app-debian-packages.txt"
printf 'Source commit: %s\nApp image used for package inventory: %s\n' \
  "$source_sha" "$app_image" > "$output_dir/notices/build-source.txt"

tar -C "$output_dir" -czf "$output_dir/folderhug-notices-$tag.tar.gz" notices
rm -rf -- "$output_dir/notices"
(
  cd "$output_dir"
  sha256sum "folderhug-compose-$tag.yaml" "folderhug-notices-$tag.tar.gz" \
    > "folderhug-checksums-$tag.txt"
  sha256sum -c "folderhug-checksums-$tag.txt"
)
