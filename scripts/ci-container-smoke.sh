#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: ci-container-smoke.sh IMAGE [EXPECTED_REVISION]}
version=$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.version"}}')
revision=$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
if [[ $# -ge 2 ]]; then
  test "$revision" = "$2"
  test "$version" = "$(tr -d '\r\n' < VERSION)"
fi
channel=stable
if [[ $version == dev ]]; then channel=development; elif [[ $version == *-rc.* ]]; then channel=prerelease; fi
# Metadata inspection runs without mounts or network access and must not initialize app state.
docker run --rm --read-only --network none "$image" version | jq -e \
  --arg version "$version" --arg revision "$revision" --arg channel "$channel" \
  'keys == ["channel", "revision", "version"] and .version == $version and .revision == $revision and .channel == $channel' >/dev/null
# The mount fixture needs disk storage, since tmpfs is not a save destination.
state_dir=$(mktemp -d "$PWD/.folderhug-ci.XXXXXX")
mount_dir="$state_dir/mount"
parent_dir="$state_dir/parent"
readonly_dir="$state_dir/readonly"
mkdir -p "$mount_dir/allowed" "$parent_dir/synthetic" "$readonly_dir"
suffix="${state_dir##*/}-$$"
data_volume="folderhug-ci-data-${suffix}"
keys_volume="folderhug-ci-keys-${suffix}"
downloads_volume="folderhug-ci-downloads-${suffix}"
cache_volume="folderhug-ci-cache-${suffix}"
container="folderhug-ci-${suffix}"
created_volumes=()
container_created=false

cleanup() {
  if [[ $container_created == true ]]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  if ((${#created_volumes[@]})); then
    docker volume rm "${created_volumes[@]}" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$state_dir"
}
trap cleanup EXIT

chmod 0777 "$mount_dir" "$mount_dir/allowed" "$parent_dir" "$parent_dir/synthetic" "$readonly_dir"
for volume in "$data_volume" "$keys_volume" "$downloads_volume" "$cache_volume"; do
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    echo "Refusing existing CI volume: $volume" >&2
    exit 1
  fi
  docker volume create "$volume" >/dev/null
  created_volumes+=("$volume")
done
if docker container inspect "$container" >/dev/null 2>&1; then
  echo "Refusing existing CI container: $container" >&2
  exit 1
fi

start_app() {
  local use_mount=$1
  local args=(
    --name "$container" -p 127.0.0.1::8080
    --read-only --cap-drop ALL --security-opt no-new-privileges
    --tmpfs /tmp:rw,size=64m,mode=1777
    --mount "type=volume,source=$data_volume,target=/data"
    --mount "type=volume,source=$keys_volume,target=/keys"
    --mount "type=volume,source=$downloads_volume,target=/downloads"
    --mount "type=volume,source=$cache_volume,target=/cache"
    --mount "type=bind,source=$parent_dir,target=/mnt"
    --mount "type=bind,source=$readonly_dir,target=/mnt/readonly,readonly"
    --mount "type=volume,source=$keys_volume,target=/mnt/private"
  )
  if [[ $use_mount == yes ]]; then
    args+=(--mount "type=bind,source=$mount_dir,target=/mnt/synthetic")
    args+=(--mount "type=volume,source=$keys_volume,target=/mnt/synthetic/blocked")
  fi
  docker run -d "${args[@]}" "$image" >/dev/null
  container_created=true

  local ready=0
  for _ in {1..30}; do
    if docker exec "$container" folderhug healthcheck >/dev/null 2>&1; then
      ready=1
      break
    fi
    if [[ $(docker inspect -f '{{.State.Running}}' "$container") != true ]]; then
      docker logs "$container" >&2
      exit 1
    fi
    sleep 1
  done
  if [[ $ready != 1 ]]; then
    docker logs "$container" >&2
    echo 'Core app did not become ready' >&2
    exit 1
  fi

  local published
  published=$(docker port "$container" 8080/tcp)
  port=${published##*:}
  base="http://127.0.0.1:$port"
}

api() {
  curl --silent --show-error --fail "$@" "$base/api/v1/destinations"
}

login() {
  curl --silent --show-error --fail \
    -c "$state_dir/cookies" \
    -H "Origin: $base" -H 'Content-Type: application/json' \
    --data '{"password":"synthetic-ci-password-2026"}' \
    "$base/api/v1/auth/login" >/dev/null
  awk '$6 == "folderhug_session" { found = 1 } END { exit !found }' "$state_dir/cookies"
}

start_app yes
docker exec "$container" http-wrapper healthcheck
curl --silent --show-error --fail \
  "$base/api/v1/auth/setup" | jq -e '.setup_required == true' >/dev/null
curl --silent --show-error --fail \
  -H "Origin: $base" -H 'Content-Type: application/json' \
  --data '{"password":"synthetic-ci-password-2026","remember_me":true}' \
  "$base/api/v1/auth/setup" | jq -e '.authenticated == true' >/dev/null
login
destinations=$(api -b "$state_dir/cookies")
mount_id=$(jq -er '.destinations[] | select(.label == "synthetic" and .available == true) | .id' <<<"$destinations")
jq -e '.destinations | length == 2' <<<"$destinations" >/dev/null
curl --silent --show-error --fail -b "$state_dir/cookies" \
  "$base/api/v1/destinations/$mount_id/folders" | \
  jq -e '.folders | length == 1 and .[0].name == "allowed"' >/dev/null
status=$(curl --silent --show-error -o /dev/null -w '%{http_code}' -b "$state_dir/cookies" \
  "$base/api/v1/destinations/$mount_id/folders?path=blocked")
[[ $status == 400 ]]

docker exec "$container" sh -c 'printf mounted > /mnt/synthetic/ci-marker && test -w /mnt/synthetic'
[[ $(cat "$mount_dir/ci-marker") == mounted ]]

docker rm -f "$container" >/dev/null
container_created=false
start_app yes
login
api -b "$state_dir/cookies" | jq -e --arg id "$mount_id" \
  '.destinations[] | select(.id == $id and .available == true)' >/dev/null

docker rm -f "$container" >/dev/null
container_created=false
start_app no
curl --silent --show-error --fail \
  "$base/api/v1/auth/setup" | jq -e '.setup_required == false' >/dev/null
login
api -b "$state_dir/cookies" | jq -e '.destinations | length == 1 and .[0].id == "server"' >/dev/null
status=$(curl --silent --show-error -o /dev/null -w '%{http_code}' -b "$state_dir/cookies" \
  "$base/api/v1/destinations/$mount_id/folders")
[[ $status == 404 ]]
[[ -z $(ls -A "$parent_dir/synthetic") ]]

echo 'Default image setup, stable mount IDs, protected folders, and missing mount checks passed.'
