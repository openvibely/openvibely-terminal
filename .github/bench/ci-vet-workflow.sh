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

export VTC_MODE="$mode"
export VTC_COVERPROFILE="$profile"
export VTC_METRICS_DIR="$metrics"
export VTC_LOG_DIR="$metrics"
export VTC_TIME_DIR="$metrics"
export VTC_VET_GOMAXPROCS="$cap"

.github/workflows/vet-test-coverage.sh
