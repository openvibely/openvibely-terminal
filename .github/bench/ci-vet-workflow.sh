#!/usr/bin/env bash
set -uo pipefail

repo=${BENCH_REPO:?}
metrics=${BENCH_METRICS:?}
cache=${BENCH_GOCACHE:?}
modcache=${GOMODCACHE:?}
profile=${BENCH_PROFILE:?}
mode=${BENCH_MODE:?}
cap=${BENCH_CAP:?}
mkdir -p "$metrics" "$cache" "$modcache"
export GOCACHE="$cache"
cd "$repo"

now_ns() { date +%s%N; }
printf '%s\n' "$(now_ns)" >"$metrics/start_ns"
vet_status=0
test_status=0

if [[ "$cap" == default ]]; then
  /usr/bin/time -p -o "$metrics/vet.time" bash -c '
    printf "%s\n" "$(date +%s%N)" >"$1/vet_start_ns"
    go vet ./...
    status=$?
    printf "%s\n" "$(date +%s%N)" >"$1/vet_done_ns"
    exit "$status"
  ' bash "$metrics" >"$metrics/vet.log" 2>&1 &
else
  GOMAXPROCS="$cap" /usr/bin/time -p -o "$metrics/vet.time" bash -c '
    printf "%s\n" "$(date +%s%N)" >"$1/vet_start_ns"
    go vet ./...
    status=$?
    printf "%s\n" "$(date +%s%N)" >"$1/vet_done_ns"
    exit "$status"
  ' bash "$metrics" >"$metrics/vet.log" 2>&1 &
fi
vet_pid=$!
printf '%s\n' "$(now_ns)" >"$metrics/test_start_ns"
if [[ "$mode" == uncached ]]; then
  /usr/bin/time -p -o "$metrics/test.time" go test ./... -count=1 -timeout 120s -coverpkg=./... -coverprofile="$profile" >"$metrics/test.log" 2>&1 || test_status=$?
else
  /usr/bin/time -p -o "$metrics/test.time" go test ./... -timeout 120s -coverpkg=./... -coverprofile="$profile" >"$metrics/test.log" 2>&1 || test_status=$?
fi
printf '%s\n' "$(now_ns)" >"$metrics/test_done_ns"
wait "$vet_pid" || vet_status=$?
printf '%s\n' "$(now_ns)" >"$metrics/end_ns"
printf '%s\n' "$vet_status" >"$metrics/vet_status"
printf '%s\n' "$test_status" >"$metrics/test_status"

if (( vet_status != 0 || test_status != 0 )); then
  exit 1
fi
[[ -s "$profile" ]] || exit 1
exit 0
