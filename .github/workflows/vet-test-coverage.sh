#!/usr/bin/env bash
set -uo pipefail

mode=${VTC_MODE:-routine}
profile=${VTC_COVERPROFILE:-coverage.txt}
coverpkg=${VTC_COVERPKG:-./...}
timeout=${VTC_TIMEOUT:-120s}
vet_pkg=${VTC_VET_PKG:-./...}
test_pkg=${VTC_TEST_PKG:-./...}
vet_gomaxprocs=${VTC_VET_GOMAXPROCS:-1}
metrics_dir=${VTC_METRICS_DIR:-}
log_dir=${VTC_LOG_DIR:-}
time_dir=${VTC_TIME_DIR:-}
annotations=${VTC_GITHUB_ANNOTATIONS:-false}

vet_status=0
test_status=0
vet_pid=
test_pid=

now_ns() { date +%s%N; }

write_metric() {
  local name=$1
  local value=$2
  if [[ -n "$metrics_dir" ]]; then
    mkdir -p "$metrics_dir"
    printf '%s\n' "$value" >"$metrics_dir/$name"
  fi
}

stop_child() {
  local pid=$1
  if [[ -z "$pid" ]]; then
    return
  fi
  kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  for _ in {1..50}; do
    if ! kill -0 "$pid" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup_children() {
  if [[ -n "${test_pid:-}" ]]; then
    stop_child "$test_pid"
    test_pid=
  fi
  if [[ -n "${vet_pid:-}" ]]; then
    stop_child "$vet_pid"
    vet_pid=
  fi
}

on_exit() {
  local status=$?
  trap - EXIT INT TERM
  cleanup_children
  exit "$status"
}

on_signal() {
  local status=$1
  trap - EXIT INT TERM
  cleanup_children
  exit "$status"
}

trap on_exit EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

run_vet() {
  write_metric vet_start_ns "$(now_ns)"
  if [[ "$vet_gomaxprocs" == default ]]; then
    go vet "$vet_pkg"
  else
    GOMAXPROCS="$vet_gomaxprocs" go vet "$vet_pkg"
  fi
  local status=$?
  write_metric vet_done_ns "$(now_ns)"
  return "$status"
}

run_test() {
  write_metric test_start_ns "$(now_ns)"
  local status=0
  if [[ "$mode" == uncached ]]; then
    go test "$test_pkg" -count=1 -timeout "$timeout" -coverpkg="$coverpkg" -coverprofile="$profile"
  else
    go test "$test_pkg" -timeout "$timeout" -coverpkg="$coverpkg" -coverprofile="$profile"
  fi
  status=$?
  write_metric test_done_ns "$(now_ns)"
  return "$status"
}

run_timed() {
  local name=$1
  shift
  if [[ -n "$time_dir" ]]; then
    mkdir -p "$time_dir"
    /usr/bin/time -p -o "$time_dir/$name.time" bash -c "$*"
  else
    "$@"
  fi
}

start_vet() {
  if [[ -n "$log_dir" ]]; then
    mkdir -p "$log_dir"
    if command -v setsid >/dev/null 2>&1; then
      setsid bash -c 'run_timed vet run_vet' >"$log_dir/vet.log" 2>&1 &
    else
      bash -c 'run_timed vet run_vet' >"$log_dir/vet.log" 2>&1 &
    fi
  else
    if command -v setsid >/dev/null 2>&1; then
      setsid bash -c 'run_vet' &
    else
      bash -c 'run_vet' &
    fi
  fi
  vet_pid=$!
}

start_test() {
  if [[ -n "$log_dir" ]]; then
    mkdir -p "$log_dir"
    if command -v setsid >/dev/null 2>&1; then
      setsid bash -c 'run_timed test run_test' >"$log_dir/test.log" 2>&1 &
    else
      bash -c 'run_timed test run_test' >"$log_dir/test.log" 2>&1 &
    fi
  else
    if command -v setsid >/dev/null 2>&1; then
      setsid bash -c 'run_test' &
    else
      bash -c 'run_test' &
    fi
  fi
  test_pid=$!
}

export -f now_ns write_metric run_vet run_test run_timed
export mode profile coverpkg timeout vet_pkg test_pkg vet_gomaxprocs metrics_dir time_dir

write_metric start_ns "$(now_ns)"
start_vet
start_test

# Always reap both children so a failure, cancellation, or timeout cannot
# hide a status or leave vet/test running.
wait "$test_pid" || test_status=$?
test_pid=
wait "$vet_pid" || vet_status=$?
vet_pid=
write_metric end_ns "$(now_ns)"
write_metric vet_status "$vet_status"
write_metric test_status "$test_status"

if (( vet_status != 0 )); then
  if [[ "$annotations" == true ]]; then
    echo "::error::go vet ./... failed with exit code ${vet_status}"
  else
    echo "go vet ./... failed with exit code ${vet_status}" >&2
  fi
fi
if (( test_status != 0 )); then
  if [[ "$annotations" == true ]]; then
    echo "::error::go test ./... failed with exit code ${test_status}"
  else
    echo "go test ./... failed with exit code ${test_status}" >&2
  fi
fi
if (( vet_status != 0 || test_status != 0 )); then
  exit 1
fi

if [[ ! -s "$profile" ]]; then
  if [[ "$annotations" == true ]]; then
    echo "::error::go test did not produce a non-empty coverage profile"
  else
    echo "go test did not produce a non-empty coverage profile" >&2
  fi
  exit 1
fi

exit 0
