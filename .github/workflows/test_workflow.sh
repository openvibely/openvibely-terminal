#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
workflow="$repo_root/.github/workflows/test.yml"
shared_script="$repo_root/.github/workflows/vet-test-coverage.sh"
bench_helper="$repo_root/.github/bench/ci-vet-workflow.sh"
real_go=$(command -v go)

fail() {
  echo "workflow validation failed: $*" >&2
  exit 1
}

[[ -x "$shared_script" ]] || fail "shared vet/test script is not executable"

extract_step_field() {
  local workflow_file=$1
  local step_name=$2
  local field_name=$3
  local indentation=$4
  local keep_field=${5:-false}

  awk \
    -v step_name="$step_name" \
    -v field_name="$field_name" \
    -v indentation="$indentation" \
    -v keep_field="$keep_field" '
    BEGIN {
      step_header = "      - name: " step_name
      field_prefix = sprintf("%*s%s: ", indentation, "", field_name)
    }
    /^      - name:/ {
      if (in_step) exit
      if ($0 == step_header) {
        in_step=1
        next
      }
    }
    in_step && index($0, field_prefix) == 1 {
      if (keep_field == "true") {
        print substr($0, indentation + 1)
      } else {
        print substr($0, length(field_prefix) + 1)
      }
      exit
    }
  ' "$workflow_file"
}

validate_workflow_fields() {
  local workflow=$1
  local verification_run mode_env annotation_env summary_run summary_if

  verification_run=$(extract_step_field "$workflow" "Run vet and all tests with coverage" run 8)
  [[ "$verification_run" == ".github/workflows/vet-test-coverage.sh" ]] ||
    fail "verification step does not call the shared script: $verification_run"

  mode_env=$(extract_step_field "$workflow" "Run vet and all tests with coverage" VTC_MODE 10)
  [[ "$mode_env" == "\${{ inputs.uncached && 'uncached' || 'routine' }}" ]] ||
    fail "workflow no longer maps uncached input to shared script mode: $mode_env"

  annotation_env=$(extract_step_field "$workflow" "Run vet and all tests with coverage" VTC_GITHUB_ANNOTATIONS 10)
  [[ "$annotation_env" == '"true"' ]] ||
    fail "workflow no longer enables GitHub error annotations"

  summary_run=$(extract_step_field "$workflow" "Show coverage summary" run 8)
  [[ "$summary_run" == "go tool cover -func=coverage.txt | tail -1" ]] ||
    fail "coverage summary command changed: $summary_run"

  summary_if=$(extract_step_field "$workflow" "Show coverage summary" if 8 true)
  [[ "$summary_if" == 'if: ${{ success() }}' ]] ||
    fail "coverage summary is not success-gated"
}

validate_workflow_fields "$workflow"

run_line=$(grep -n '^      - name: Run vet and all tests with coverage$' "$workflow" | cut -d: -f1)
summary_line=$(grep -n '^      - name: Show coverage summary$' "$workflow" | cut -d: -f1)
if (( run_line >= summary_line )); then
  fail "coverage summary is not after verification"
fi

if grep -Eq 'go (vet|test) ./\.\.\.' "$workflow"; then
  fail "workflow duplicates vet/test command bodies instead of calling the shared script"
fi
[[ "$(grep -cF '.github/workflows/vet-test-coverage.sh' "$bench_helper")" -eq 1 ]] ||
  fail "benchmark helper does not call the shared script exactly once"
if grep -Eq 'go (vet|test) ./\.\.\.' "$bench_helper"; then
  fail "benchmark helper duplicates vet/test command bodies instead of calling the shared script"
fi
for required_export in VTC_MODE VTC_COVERPROFILE VTC_METRICS_DIR VTC_LOG_DIR VTC_TIME_DIR VTC_VET_GOMAXPROCS; do
  grep -Fq "export $required_export=" "$bench_helper" ||
    fail "benchmark helper does not configure $required_export"
done

test_root=$(mktemp -d "${TMPDIR:-/tmp}/openvibely-workflow-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT

validator_fixture="$test_root/validator-workflow.yml"
validator_log="$test_root/validator.log"
cat >"$validator_fixture" <<'VALIDATOR_WORKFLOW'
      - name: Run vet and all tests with coverage
        run: .github/workflows/vet-test-coverage.sh
        env:
          VTC_MODE: ${{ inputs.uncached && 'uncached' || 'routine' }}
          VTC_GITHUB_ANNOTATIONS: "true"
      - name: Show coverage summary
        if: ${{ success() }}
        run: go tool cover -func=coverage.txt | tail -1
      - name: Later step with duplicate verification fields
        run: .github/workflows/vet-test-coverage.sh
        env:
          VTC_MODE: ${{ inputs.uncached && 'uncached' || 'routine' }}
          VTC_GITHUB_ANNOTATIONS: "true"
      - name: Later step with duplicate coverage fields
        if: ${{ success() }}
        run: go tool cover -func=coverage.txt | tail -1
VALIDATOR_WORKFLOW

replace_fixture_once() {
  python3 - "$1" "$2" "$3" <<'PY'
from pathlib import Path
import sys

path, old, new = sys.argv[1:]
content = Path(path).read_text()
if old not in content:
    raise SystemExit(f"fixture text not found: {old!r}")
Path(path).write_text(content.replace(old, new, 1))
PY
}

assert_validator_rejects() {
  local case_name=$1
  local expected_message=$2
  if (validate_workflow_fields "$validator_fixture") >"$validator_log" 2>&1; then
    fail "$case_name unexpectedly passed workflow validation"
  fi
  grep -Fq "workflow validation failed: $expected_message" "$validator_log" ||
    fail "$case_name reported an unexpected validation failure: $(<"$validator_log")"
}

run_validator_regression() {
  local case_name=$1
  local old_line=$2
  local new_line=$3
  local expected_message=$4
  cp "$test_root/validator-workflow-baseline.yml" "$validator_fixture"
  replace_fixture_once "$validator_fixture" "$old_line" "$new_line"
  assert_validator_rejects "$case_name" "$expected_message"
}

cp "$validator_fixture" "$test_root/validator-workflow-baseline.yml"
validate_workflow_fields "$validator_fixture"

boundary_fixture="$test_root/step-boundary.yml"
cat >"$boundary_fixture" <<'STEP_BOUNDARY'
      - name: Boundary target
      - name: Separator
      - name: Later step with matching fields
        run: .github/workflows/vet-test-coverage.sh
        env:
          VTC_MODE: ${{ inputs.uncached && 'uncached' || 'routine' }}
          VTC_GITHUB_ANNOTATIONS: "true"
        if: ${{ success() }}
STEP_BOUNDARY
assert_extractor_empty() {
  local case_name=$1
  local field_name=$2
  local indentation=$3
  local keep_field=${4:-false}
  local actual
  actual=$(extract_step_field "$boundary_fixture" "Boundary target" "$field_name" "$indentation" "$keep_field")
  [[ -z "$actual" ]] || fail "$case_name crossed a step boundary and returned: $actual"
}
assert_extractor_empty "verification command lookup" run 8
assert_extractor_empty "mode lookup" VTC_MODE 10
assert_extractor_empty "annotation lookup" VTC_GITHUB_ANNOTATIONS 10
assert_extractor_empty "coverage command lookup" run 8
assert_extractor_empty "coverage condition lookup" if 8 true

cat >"$boundary_fixture" <<'REPEATED_STEP_BOUNDARY'
      - name: Boundary target
      - name: Boundary target
        run: later duplicate
REPEATED_STEP_BOUNDARY
assert_extractor_empty "repeated step-name lookup" run 8

run_validator_regression \
  "verification command mismatch" \
  "        run: .github/workflows/vet-test-coverage.sh" \
  "        run: go vet ./..." \
  "verification step does not call the shared script: go vet ./..."
run_validator_regression \
  "mode mismatch" \
  "          VTC_MODE: \${{ inputs.uncached && 'uncached' || 'routine' }}" \
  "          VTC_MODE: wrong" \
  "workflow no longer maps uncached input to shared script mode: wrong"
run_validator_regression \
  "annotation mismatch" \
  '          VTC_GITHUB_ANNOTATIONS: "true"' \
  '          VTC_GITHUB_ANNOTATIONS: "false"' \
  "workflow no longer enables GitHub error annotations"
run_validator_regression \
  "coverage command mismatch" \
  "        run: go tool cover -func=coverage.txt | tail -1" \
  "        run: echo wrong" \
  "coverage summary command changed: echo wrong"
run_validator_regression \
  "coverage condition mismatch" \
  '        if: ${{ success() }}' \
  '        if: ${{ always() }}' \
  "coverage summary is not success-gated"
run_validator_regression \
  "verification command missing with later duplicate" \
  "        run: .github/workflows/vet-test-coverage.sh" \
  "" \
  "verification step does not call the shared script: "
run_validator_regression \
  "mode missing with later duplicate" \
  "          VTC_MODE: \${{ inputs.uncached && 'uncached' || 'routine' }}" \
  "" \
  "workflow no longer maps uncached input to shared script mode: "
run_validator_regression \
  "annotations missing with later duplicate" \
  '          VTC_GITHUB_ANNOTATIONS: "true"' \
  "" \
  "workflow no longer enables GitHub error annotations"
run_validator_regression \
  "coverage command missing with later duplicate" \
  "        run: go tool cover -func=coverage.txt | tail -1" \
  "" \
  "coverage summary command changed: "
run_validator_regression \
  "coverage condition missing with later duplicate" \
  '        if: ${{ success() }}' \
  "" \
  "coverage summary is not success-gated"

fake_bin="$test_root/bin"
mkdir -p "$fake_bin"
original_path=$PATH

cat >"$fake_bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u

log=${FAKE_LOG:?}
run_id=${FAKE_RUN_ID:?}

child_pid=
stop_child() {
  local status=$1
  if [[ -n "${child_pid:-}" ]]; then
    kill -TERM "$child_pid" 2>/dev/null || true
    wait "$child_pid" 2>/dev/null || true
    child_pid=
  fi
  exit "$status"
}
trap 'stop_child 130' INT
trap 'stop_child 143' TERM

case "${1-}" in
  vet)
    if [[ "$#" -ne 2 || "$2" != "./..." ]]; then
      echo "unexpected vet arguments" >&2
      exit 97
    fi
    if [[ "${FAKE_EXPECT_VET_GOMAXPROCS:-1}" == default ]]; then
      if [[ -n "${GOMAXPROCS-}" ]]; then
        echo "vet must not override GOMAXPROCS in default cap mode" >&2
        exit 98
      fi
    elif [[ "${GOMAXPROCS-}" != "${FAKE_EXPECT_VET_GOMAXPROCS:-1}" ]]; then
      echo "vet must use GOMAXPROCS=${FAKE_EXPECT_VET_GOMAXPROCS:-1} while overlapping" >&2
      exit 98
    fi
    printf 'vet-start-%s\n' "$run_id" >>"$log"
    printf '%s\n' "$$" >"$log.vet-pid"
    sleep "${FAKE_VET_DELAY:-0.25}" &
    child_pid=$!
    printf '%s\n' "$child_pid" >"$log.vet-child-pid"
    wait "$child_pid"
    child_pid=
    printf 'vet-done-%s\n' "$run_id" >>"$log"
    exit "${FAKE_VET_STATUS:-0}"
    ;;
  test)
    if [[ "$#" -lt 2 || "$2" != "./..." ]]; then
      echo "unexpected test arguments" >&2
      exit 97
    fi
    count=0
    timeout=0
    coverpkg=0
    profile=
    for ((i = 1; i <= $#; i++)); do
      arg=${!i}
      case "$arg" in
        -count=1) count=1 ;;
        -timeout)
          next_index=$((i + 1))
          [[ "${!next_index-}" == "120s" ]] || exit 99
          timeout=1
          ;;
        -coverpkg=./...) coverpkg=1 ;;
        -coverprofile=*) profile=${arg#-coverprofile=} ;;
      esac
    done
    if (( timeout != 1 || coverpkg != 1 )) || [[ -z "${profile}" ]]; then
      echo "coverage test flags changed" >&2
      exit 99
    fi
    if [[ "${FAKE_UNCACHED:-true}" == true && "$count" -ne 1 ]] ||
      [[ "${FAKE_UNCACHED:-true}" == false && "$count" -ne 0 ]]; then
      echo "cache-mode test flag changed" >&2
      exit 99
    fi
    printf 'test-start-%s\n' "$run_id" >>"$log"
    printf '%s\n' "$$" >"$log.test-pid"
    sleep "${FAKE_TEST_DELAY:-0.01}" &
    child_pid=$!
    printf '%s\n' "$child_pid" >"$log.test-child-pid"
    wait "$child_pid"
    child_pid=
    if [[ "${FAKE_EMPTY_COVERAGE:-false}" == true ]]; then
      : >"$profile"
    else
      printf 'mode: set\n%s:3.1,3.17 1 1\n' "${FAKE_SOURCE:?}" >"$profile"
    fi
    printf 'test-done-%s\n' "$run_id" >>"$log"
    exit "${FAKE_TEST_STATUS:-0}"
    ;;
  *)
    echo "unexpected go command: $*" >&2
    exit 96
    ;;
esac
FAKE_GO
chmod +x "$fake_bin/go"

cat >"$fake_bin/setsid" <<'FAKE_SETSID'
#!/usr/bin/env python3
import os
import signal
import sys

for signum in (signal.SIGINT, signal.SIGTERM):
    signal.signal(signum, signal.SIG_DFL)

try:
    os.setsid()
except PermissionError:
    child = os.fork()
    if child:
        _, status = os.waitpid(child, 0)
        sys.exit(os.waitstatus_to_exitcode(status))
    os.setsid()
os.execvp(sys.argv[1], sys.argv[1:])
FAKE_SETSID
chmod +x "$fake_bin/setsid"

line_for() {
  local event=$1
  local log=$2
  awk -v event="$event" '$0 == event { print NR; exit }' "$log"
}

run_shared() {
  local case_dir=$1
  local run_id=$2
  local vet_status=$3
  local test_status=$4
  local uncached=${5:-true}
  local vet_cap=${6:-1}
  local empty_coverage=${7:-false}
  local status=0
  local log="$case_dir/events.log"
  local workflow_log="$case_dir/workflow-$run_id.log"
  local mode=routine
  [[ "$uncached" == true ]] && mode=uncached

  if (
    cd "$case_dir"
    FAKE_LOG="$log" \
    FAKE_RUN_ID="$run_id" \
    FAKE_SOURCE="$case_dir/fixture.go" \
    FAKE_VET_STATUS="$vet_status" \
    FAKE_TEST_STATUS="$test_status" \
    FAKE_UNCACHED="$uncached" \
    FAKE_EXPECT_VET_GOMAXPROCS="$vet_cap" \
    FAKE_EMPTY_COVERAGE="$empty_coverage" \
    FAKE_VET_DELAY=0.25 \
    FAKE_TEST_DELAY=0.01 \
    VTC_MODE="$mode" \
    VTC_VET_GOMAXPROCS="$vet_cap" \
    VTC_GITHUB_ANNOTATIONS=true \
    PATH="$fake_bin:$original_path" \
      "$shared_script" >"$workflow_log" 2>&1
  ); then
    status=0
  else
    status=$?
  fi
  printf 'runner-returned-%s\n' "$run_id" >>"$log"

  if (( vet_status != 0 )) &&
    ! grep -Fq "::error::go vet ./... failed with exit code $vet_status" "$workflow_log"; then
    fail "$run_id did not report the vet failure"
  fi
  if (( test_status != 0 )) &&
    ! grep -Fq "::error::go test ./... failed with exit code $test_status" "$workflow_log"; then
    fail "$run_id did not report the test failure"
  fi
  if [[ "$empty_coverage" == true ]] &&
    ! grep -Fq "::error::go test did not produce a non-empty coverage profile" "$workflow_log"; then
    fail "$run_id did not report an empty coverage profile"
  fi

  local vet_done test_done returned
  vet_done=$(line_for "vet-done-$run_id" "$log")
  test_done=$(line_for "test-done-$run_id" "$log")
  returned=$(line_for "runner-returned-$run_id" "$log")
  [[ -n "$vet_done" ]] || fail "$run_id did not finish vet"
  [[ -n "$test_done" ]] || fail "$run_id did not finish tests"
  [[ -n "$returned" ]] || fail "$run_id did not return"
  if (( vet_done >= returned )); then
    fail "$run_id returned before vet was reaped"
  fi
  if (( test_status != 0 && test_done >= vet_done )); then
    fail "$run_id did not overlap a failing test with vet"
  fi
  RUN_STATUS=$status
}

make_fixture() {
  local case_dir=$1
  mkdir -p "$case_dir"
  cat >"$case_dir/fixture.go" <<'EOF_FIXTURE'
package fixture

func Covered() {}
EOF_FIXTURE
}

run_case() {
  local name=$1
  local vet_status=$2
  local test_status=$3
  local expected=$4
  local uncached=${5:-true}
  local vet_cap=${6:-1}
  local empty_coverage=${7:-false}
  local case_dir="$test_root/$name"
  make_fixture "$case_dir"
  run_shared "$case_dir" "$name" "$vet_status" "$test_status" "$uncached" "$vet_cap" "$empty_coverage"
  if [[ "$RUN_STATUS" -ne "$expected" ]]; then
    fail "$name returned $RUN_STATUS, expected $expected"
  fi
}

run_interrupted_shared() {
  local name=$1
  local signal=$2
  local expected=$3
  local case_dir="$test_root/$name"
  local log="$case_dir/events.log"
  local workflow_log="$case_dir/workflow.log"
  local workflow_pid
  local status=0
  local vet_pid test_pid vet_child_pid test_child_pid

  make_fixture "$case_dir"
  (
    cd "$case_dir"
    FAKE_LOG="$log" \
    FAKE_RUN_ID="$name" \
    FAKE_SOURCE="$case_dir/fixture.go" \
    FAKE_VET_STATUS=0 \
    FAKE_TEST_STATUS=0 \
    FAKE_UNCACHED=true \
    FAKE_EXPECT_VET_GOMAXPROCS=1 \
    FAKE_VET_DELAY=5 \
    FAKE_TEST_DELAY=5 \
    VTC_MODE=uncached \
    PATH="$fake_bin:$original_path" \
      exec setsid "$shared_script" >"$workflow_log" 2>&1
  ) &
  workflow_pid=$!

  for _ in {1..100}; do
    if [[ -s "$log.vet-pid" && -s "$log.test-pid" ]]; then
      break
    fi
    sleep 0.01
  done
  [[ -s "$log.vet-pid" && -s "$log.test-pid" ]] ||
    fail "$name did not start both children before interruption"

  kill -"$signal" "$workflow_pid" 2>/dev/null || fail "$name signal failed"
  if wait "$workflow_pid"; then
    status=0
  else
    status=$?
  fi
  [[ "$status" -eq "$expected" ]] ||
    fail "$name returned $status, expected $expected"

  vet_pid=$(<"$log.vet-pid")
  test_pid=$(<"$log.test-pid")
  vet_child_pid=$(<"$log.vet-child-pid")
  test_child_pid=$(<"$log.test-child-pid")
  assert_stopped() {
    local pid=$1
    for _ in {1..100}; do
      if ! kill -0 "$pid" 2>/dev/null; then
        return
      fi
      sleep 0.01
    done
    fail "$name left process $pid running after $signal"
  }
  assert_stopped "$vet_pid"
  assert_stopped "$test_pid"
  assert_stopped "$vet_child_pid"
  assert_stopped "$test_child_pid"
}

run_benchmark_helper_case() {
  local case_dir="$test_root/benchmark-helper"
  local metrics="$case_dir/metrics"
  local cache="$case_dir/cache"
  local modcache="$case_dir/modcache"
  make_fixture "$case_dir"
  if ! (
    cd "$case_dir"
    FAKE_LOG="$case_dir/events.log" \
    FAKE_RUN_ID=benchmark-helper \
    FAKE_SOURCE="$case_dir/fixture.go" \
    FAKE_VET_STATUS=0 \
    FAKE_TEST_STATUS=0 \
    FAKE_UNCACHED=false \
    FAKE_EXPECT_VET_GOMAXPROCS=default \
    BENCH_REPO="$repo_root" \
    BENCH_METRICS="$metrics" \
    BENCH_GOCACHE="$cache" \
    GOMODCACHE="$modcache" \
    BENCH_PROFILE="$case_dir/bench-coverage.txt" \
    BENCH_MODE=routine \
    BENCH_CAP=default \
    PATH="$fake_bin:$original_path" \
      "$bench_helper" >"$case_dir/bench.stdout" 2>"$case_dir/bench.stderr"
  ); then
    fail "benchmark helper did not succeed"
  fi
  for metric in start_ns vet_start_ns vet_done_ns test_start_ns test_done_ns end_ns vet_status test_status vet.time test.time; do
    [[ -s "$metrics/$metric" ]] || fail "benchmark helper did not record $metric"
  done
  for log_file in vet.log test.log; do
    [[ -e "$metrics/$log_file" ]] || fail "benchmark helper did not create $log_file"
  done
  [[ "$(<"$metrics/vet_status")" == 0 ]] || fail "benchmark helper recorded nonzero vet status"
  [[ "$(<"$metrics/test_status")" == 0 ]] || fail "benchmark helper recorded nonzero test status"
  [[ -s "$case_dir/bench-coverage.txt" ]] || fail "benchmark helper coverage profile is empty"
  "$real_go" tool cover -func="$case_dir/bench-coverage.txt" >/dev/null ||
    fail "benchmark helper coverage profile is not readable"
}

run_case clean-success 0 0 0
run_case vet-only-failure 7 0 1
run_case test-only-failure 0 9 1
run_case dual-failure 7 9 1
run_case routine-cacheable-flags 0 0 0 false
run_case uncached-flags 0 0 0 true
run_case default-vet-cap 0 0 0 true default
run_case empty-coverage 0 0 1 true 1 true
run_interrupted_shared cancellation TERM 143
run_interrupted_shared interrupt INT 130
run_interrupted_shared timeout TERM 143
run_benchmark_helper_case

repeated_dir="$test_root/repeated-coverage"
make_fixture "$repeated_dir"
run_shared "$repeated_dir" repeated-1 0 0
[[ "$RUN_STATUS" -eq 0 ]] || fail "first repeated coverage run failed"
[[ -s "$repeated_dir/coverage.txt" ]] || fail "first coverage profile is empty"
"$real_go" tool cover -func="$repeated_dir/coverage.txt" >"$repeated_dir/summary-1"
cp "$repeated_dir/coverage.txt" "$repeated_dir/coverage-first.txt"

run_shared "$repeated_dir" repeated-2 0 0
[[ "$RUN_STATUS" -eq 0 ]] || fail "second repeated coverage run failed"
[[ -s "$repeated_dir/coverage.txt" ]] || fail "second coverage profile is empty"
cmp -s "$repeated_dir/coverage-first.txt" "$repeated_dir/coverage.txt" ||
  fail "repeated coverage output was not stable"
[[ "$(grep -c '^mode: set$' "$repeated_dir/coverage.txt")" -eq 1 ]] ||
  fail "repeated coverage output contains multiple mode headers"
"$real_go" tool cover -func="$repeated_dir/coverage.txt" >"$repeated_dir/summary-2"
[[ -s "$repeated_dir/summary-1" && -s "$repeated_dir/summary-2" ]] ||
  fail "coverage summary output is empty"

echo "workflow validation passed"
