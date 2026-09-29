#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: ci-container-smoke.sh IMAGE}
state_dir=$(mktemp -d)
mount_dir="$state_dir/mount"
mkdir "$mount_dir"
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

chmod 0777 "$mount_dir"
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

printf '%s\n' 'synthetic-ci-password-2026' | docker run --rm -i \
  --network none \
  --mount "type=volume,source=$data_volume,target=/data" \
  --mount "type=volume,source=$keys_volume,target=/keys" \
  -e FOLDERHUG_DATA_DIR=/data \
  -e FOLDERHUG_KEYS_DIR=/keys \
  "$image" setup --password-stdin >/dev/null

start_app() {
  local use_mount=$1
  local args=(
    --name "$container" -p 127.0.0.1::8080
    --read-only --cap-drop ALL --security-opt no-new-privileges
    --tmpfs /tmp:rw,size=64m,mode=1777
    --mount "type=volume,source=$data_volume,target=/data"
    --mount "type=volume,source=$keys_volume,target=/keys,readonly"
    --mount "type=volume,source=$downloads_volume,target=/downloads"
    --mount "type=volume,source=$cache_volume,target=/cache"
    -e FOLDERHUG_ADDR=0.0.0.0:8080
    -e FOLDERHUG_PUBLIC_URL=http://localhost:8080
    -e FOLDERHUG_DATA_DIR=/data
    -e FOLDERHUG_KEYS_DIR=/keys
    -e FOLDERHUG_DOWNLOADS_DIR=/downloads
    -e FOLDERHUG_CACHE_DIR=/cache
    -e 'FOLDERHUG_MOUNTED_ROOTS=[{"id":"synthetic","label":"Synthetic","path":"/mnt/synthetic"}]'
  )
  if [[ $use_mount == yes ]]; then
    args+=(--mount "type=bind,source=$mount_dir,target=/mnt/synthetic")
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
}

api() {
  curl --silent --show-error --fail \
    --connect-to "localhost:8080:127.0.0.1:$port" \
    "$@" "http://localhost:8080/api/v1/destinations"
}

login() {
  curl --silent --show-error --fail \
    --connect-to "localhost:8080:127.0.0.1:$port" \
    -c "$state_dir/cookies" \
    -H 'Origin: http://localhost:8080' \
    -H 'Content-Type: application/json' \
    --data '{"password":"synthetic-ci-password-2026"}' \
    'http://localhost:8080/api/v1/auth/login' >/dev/null
  awk '$6 == "folderhug_session" { found = 1 } END { exit !found }' "$state_dir/cookies"
}

start_app yes
docker exec "$container" http-wrapper healthcheck
login
api -b "$state_dir/cookies" | jq -e '.destinations[] | select(.id == "synthetic" and .available == true)' >/dev/null
docker exec "$container" sh -c 'printf mounted > /mnt/synthetic/ci-marker && test -w /mnt/synthetic'
[[ $(cat "$mount_dir/ci-marker") == mounted ]]

docker rm -f "$container" >/dev/null
container_created=false
start_app no
login
api -b "$state_dir/cookies" | jq -e '.destinations[] | select(.id == "synthetic" and .available == false)' >/dev/null
echo 'Core startup, restart, and synthetic mount smoke passed.'
