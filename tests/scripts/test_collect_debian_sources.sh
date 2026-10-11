#!/usr/bin/env bash
set -euo pipefail

fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/bin" "$fixture/apt"
export COLLECTOR_FIXTURE="$fixture"
cat > "$fixture/bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *'--entrypoint dpkg-query'*)
    printf 'libpcre2-8-0\t10.42-1+deb12u1\tpcre2\t10.42-1+deb12u1\n'
    printf 'libpcre2-posix3\t10.42-1+deb12u1\tpcre2\t10.42-1+deb12u1\n'
    printf 'tzdata\t2026b-0+deb12u1\ttzdata\t2026b-0+deb12u1\n'
    exit ;;
  *'--entrypoint cat'*)
    cat <<'EOF'
Types: deb
# http://snapshot.debian.org/archive/debian-security/20260918T000000Z
URIs: http://deb.debian.org/debian-security
Suites: bookworm-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOF
    exit ;;
esac
for arg in "$@"; do
  if [[ $arg == type=bind,source=*,target=/work ]]; then
    target=${arg#type=bind,source=}
    target=${target%,target=/work}
    code=${!#}
    code=${code//\/etc\/apt\/sources.list.d/$COLLECTOR_FIXTURE\/apt}
    code=${code//\/work/$target}
    bash -ec "$code"
    exit
  fi
done
echo 'Unexpected Docker invocation' >&2
exit 1
SH
cat > "$fixture/bin/apt-get" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$COLLECTOR_FIXTURE/apt-calls"
snapshot="$COLLECTOR_FIXTURE/apt/folderhug-snapshot.sources"
error_mode=''
if [[ $1 == -o ]]; then
  test "$2" = APT::Update::Error-Mode=any
  error_mode=any
  shift 2
fi
if [[ $1 == update ]]; then
  if [[ -e $snapshot ]]; then
    grep -Fx 'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' "$snapshot"
    grep -Fx 'URIs: https://snapshot.debian.org/archive/debian-security/20260918T000000Z/' "$snapshot"
    grep -Fx 'Check-Valid-Until: no' "$snapshot"
    if [[ $COLLECTOR_MODE == invalid-index ]]; then
      echo 'The following signatures could not be verified' >&2
      if [[ $error_mode == any ]]; then
        exit 100
      fi
      exit 0
    fi
  fi
  exit
fi
test "$1 $2 $3" = 'source --download-only --only-source'
case $4 in
  'pcre2=10.42-1+deb12u1')
    if [[ ! -e $snapshot || $COLLECTOR_MODE == unavailable ]]; then
      echo 'Exact version missing from source indexes' >&2
      exit 100
    fi
    printf 'Version: 10.42-1+deb12u1\n' > pcre2_10.42-1+deb12u1.dsc ;;
  'tzdata=2026b-0+deb12u1')
    printf 'Version: 2026b-0+deb12u1\n' > tzdata_2026b-0+deb12u1.dsc ;;
  *) echo 'Unexpected source version' >&2; exit 1 ;;
esac
SH
chmod +x "$fixture/bin/docker" "$fixture/bin/apt-get"
export PATH="$fixture/bin:$PATH"
export COLLECTOR_MODE=success
mkdir -p .local
output=$(mktemp -d .local/collector-output.XXXXXX)
rmdir "$output"
trap 'rm -rf -- "$fixture" "$output" "$output-unavailable" "$output-invalid-index"' EXIT
bash scripts/collect-debian-sources.sh folderhug:fixture "$output"
test -s "$output/sources/pcre2_10.42-1+deb12u1.dsc"
test -s "$output/sources/tzdata_2026b-0+deb12u1.dsc"
test "$(wc -l < "$output/source-packages.tsv")" = 2
test "$(grep -Fc 'source --download-only --only-source pcre2=10.42-1+deb12u1' "$fixture/apt-calls")" = 2
test "$(grep -Fc 'source --download-only --only-source tzdata=2026b-0+deb12u1' "$fixture/apt-calls")" = 1
for mode in unavailable invalid-index; do
  rm -f "$fixture/apt/folderhug-snapshot.sources"
  export COLLECTOR_MODE="$mode"
  if bash scripts/collect-debian-sources.sh folderhug:fixture "$output-$mode" > "$fixture/$mode.log" 2>&1; then
    echo "Collector accepted $mode" >&2
    exit 1
  fi
done
echo 'Collector exact-version snapshot fallback and failure refusal passed.'
