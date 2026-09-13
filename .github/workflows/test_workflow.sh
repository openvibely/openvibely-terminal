#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
workflow="$repo_root/.github/workflows/test.yml"
real_go=$(command -v go)

fail() {
  echo "workflow validation failed: $*" >&2
  exit 1
}

verification_run=$(awk '
  /^      - name: Run vet and all tests with coverage$/ { found=1; next }
  found && /^        run: \|$/ { in_run=1; next }
  found && in_run && /^      - name:/ { exit }
  found && in_run {
    if ($0 == "") { print ""; next }
    if ($0 !~ /^          /) { exit }
    sub(/^          /, "")
    print
  }
' "$workflow")
[[ -n "$verification_run" ]] || fail "could not extract the verification step"

summary_run=$(awk '
  /^      - name: Show coverage summary$/ { found=1; next }
  found && /^        run: / {
    sub(/^        run: /, "")
    print
    exit
  }
  found && /^      - name:/ { exit }
' "$workflow")
[[ "$summary_run" == "go tool cover -func=coverage.txt | tail -1" ]] ||
  fail "coverage summary command changed: $summary_run"

summary_if=$(awk '
  /^      - name: Show coverage summary$/ { found=1; next }
  found && /^        if: / {
    sub(/^        /, "")
    print
    exit
  }
  found && /^      - name:/ { exit }
' "$workflow")
[[ "$summary_if" == 'if: ${{ success() }}' ]] ||
  fail "coverage summary is not success-gated"

run_line=$(grep -n '^      - name: Run vet and all tests with coverage$' "$workflow" | cut -d: -f1)
summary_line=$(grep -n '^      - name: Show coverage summary$' "$workflow" | cut -d: -f1)
if (( run_line >= summary_line )); then
  fail "coverage summary is not after verification"
fi

for required in \
  'GOMAXPROCS=1 setsid go vet ./... &' \
  'vet_pid=$!' \
  'setsid go test ./... -count=1 -timeout 120s -coverpkg=./... -coverprofile=coverage.txt &' \
  'test_pid=$!' \
  'wait "$test_pid" || test_status=$?' \
  'wait "$vet_pid" || vet_status=$?' \
  'trap on_exit EXIT' \
  'trap '\''on_signal 130'\'' INT' \
  'trap '\''on_signal 143'\'' TERM' \
  'if (( vet_status != 0 || test_status != 0 )); then' \
  'exit 1'; do
  if ! grep -Fq -- "$required" <<<"$verification_run"; then
    fail "verification step is missing: $required"
  fi
done

if grep -Fq 'always()' <<<"$summary_if"; then
  fail "coverage summary must not run after a failed verification"
fi

test_root=$(mktemp -d "${TMPDIR:-/tmp}/openvibely-workflow-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT
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
    if [[ "${GOMAXPROCS-}" != "1" ]]; then
      echo "vet must use GOMAXPROCS=1 while overlapping" >&2
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
    for arg in "$@"; do
      case "$arg" in
        -count=1) count=1 ;;
        -timeout) timeout=1 ;;
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
    printf 'mode: set\n%s:3.1,3.17 1 1\n' "${FAKE_SOURCE:?}" >"$profile"
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

run_workflow() {
  local case_dir=$1
  local run_id=$2
  local vet_status=$3
  local test_status=$4
  local uncached=${5:-true}
  local status=0
  local log="$case_dir/events.log"
  local workflow_log="$case_dir/workflow-$run_id.log"
  local rendered_run

  rendered_run=${verification_run//\$\{\{ inputs.uncached \}\}/$uncached}

  if (
    cd "$case_dir"
    FAKE_LOG="$log" \
    FAKE_RUN_ID="$run_id" \
    FAKE_SOURCE="$case_dir/fixture.go" \
    FAKE_VET_STATUS="$vet_status" \
    FAKE_TEST_STATUS="$test_status" \
    FAKE_UNCACHED="$uncached" \
    FAKE_VET_DELAY=0.25 \
    FAKE_TEST_DELAY=0.01 \
    PATH="$fake_bin:$original_path" \
      bash -euo pipefail -c "$rendered_run" >"$workflow_log" 2>&1
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
  local case_dir="$test_root/$name"
  make_fixture "$case_dir"
  run_workflow "$case_dir" "$name" "$vet_status" "$test_status" "$uncached"
  if [[ "$RUN_STATUS" -ne "$expected" ]]; then
    fail "$name returned $RUN_STATUS, expected $expected"
  fi
}

run_interrupted_workflow() {
  local name=$1
  local signal=$2
  local expected=$3
  local case_dir="$test_root/$name"
  local log="$case_dir/events.log"
  local workflow_log="$case_dir/workflow.log"
  local rendered_run
  local workflow_pid
  local status=0
  local vet_pid test_pid vet_child_pid test_child_pid

  make_fixture "$case_dir"
  rendered_run=${verification_run//\$\{\{ inputs.uncached \}\}/true}
  (
    cd "$case_dir"
    FAKE_LOG="$log" \
    FAKE_RUN_ID="$name" \
    FAKE_SOURCE="$case_dir/fixture.go" \
    FAKE_VET_STATUS=0 \
    FAKE_TEST_STATUS=0 \
    FAKE_UNCACHED=true \
    FAKE_VET_DELAY=5 \
    FAKE_TEST_DELAY=5 \
    PATH="$fake_bin:$original_path" \
      exec setsid bash -euo pipefail -c "$rendered_run" >"$workflow_log" 2>&1
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

run_case clean-success 0 0 0
run_case vet-only-failure 7 0 1
run_case test-only-failure 0 9 1
run_case dual-failure 7 9 1
run_case routine-cacheable-flags 0 0 0 false
run_interrupted_workflow cancellation TERM 143
run_interrupted_workflow interrupt INT 130
run_interrupted_workflow timeout TERM 143

repeated_dir="$test_root/repeated-coverage"
make_fixture "$repeated_dir"
run_workflow "$repeated_dir" repeated-1 0 0
[[ "$RUN_STATUS" -eq 0 ]] || fail "first repeated coverage run failed"
[[ -s "$repeated_dir/coverage.txt" ]] || fail "first coverage profile is empty"
"$real_go" tool cover -func="$repeated_dir/coverage.txt" >"$repeated_dir/summary-1"
cp "$repeated_dir/coverage.txt" "$repeated_dir/coverage-first.txt"

run_workflow "$repeated_dir" repeated-2 0 0
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
