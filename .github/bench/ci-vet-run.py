#!/usr/bin/env python3
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

repo = Path(os.environ["BENCH_REPO"])
out = Path(os.environ["BENCH_OUT"])
cap = os.environ["BENCH_CAP"]
mode = os.environ["BENCH_MODE"]
run = int(os.environ["BENCH_RUN"])
cache = Path(os.environ["BENCH_GOCACHE"])
run_dir = out / f"{cap}-{mode}" / f"run-{run:02d}"
metrics = run_dir / "metrics"
profile = run_dir / "coverage.txt"
metrics.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
env.update({
    "BENCH_REPO": str(repo),
    "BENCH_METRICS": str(metrics),
    "BENCH_GOCACHE": str(cache),
    "BENCH_PROFILE": str(profile),
    "BENCH_MODE": mode,
    "BENCH_CAP": cap,
})
start = time.monotonic_ns()
proc = subprocess.Popen(
    [str(repo / ".github/bench/ci-vet-workflow.sh")],
    cwd=repo,
    env=env,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    start_new_session=True,
)
try:
    pgid = os.getpgid(proc.pid)
except ProcessLookupError:
    pgid = proc.pid
peak_kb = 0
peak_sample_ns = None
while proc.poll() is None:
    try:
        rows = subprocess.check_output(["ps", "-eo", "pid=,pgid=,rss="], text=True)
        total = sum(
            int(fields[2])
            for line in rows.splitlines()
            if len((fields := line.split())) >= 3 and fields[1] == str(pgid)
        )
        if total > peak_kb:
            peak_kb = total
            peak_sample_ns = time.time_ns()
    except (subprocess.CalledProcessError, ValueError):
        pass
    time.sleep(0.05)
stdout, stderr = proc.communicate()
end = time.monotonic_ns()
(run_dir / "stdout.log").write_text(stdout)
(run_dir / "stderr.log").write_text(stderr)

def read(path):
    return Path(path).read_text().strip()

def timed(path):
    values = {}
    for line in Path(path).read_text().splitlines():
        key, value = line.split(maxsplit=1)
        values[key] = float(value)
    return values

profile_bytes = profile.stat().st_size if profile.exists() else 0
cover_output = ""
cover_status = 1
if profile_bytes:
    cover = subprocess.run(
        ["go", "tool", "cover", f"-func={profile}"],
        cwd=repo,
        text=True,
        capture_output=True,
        check=False,
    )
    cover_output = cover.stdout
    cover_status = cover.returncode
coverage = None
for line in reversed(cover_output.splitlines()):
    match = re.search(r"([0-9]+(?:\.[0-9]+)?)%", line)
    if match:
        coverage = float(match.group(1))
        break
record = {
    "cap": cap,
    "mode": mode,
    "run": run,
    "status": proc.returncode,
    "vet_status": int(read(metrics / "vet_status")) if (metrics / "vet_status").exists() else None,
    "test_status": int(read(metrics / "test_status")) if (metrics / "test_status").exists() else None,
    "wrapper_wall_s": (end - start) / 1e9,
    "vet_done_s": (int(read(metrics / "vet_done_ns")) - int(read(metrics / "start_ns"))) / 1e9 if (metrics / "vet_done_ns").exists() else None,
    "test_done_s": (int(read(metrics / "test_done_ns")) - int(read(metrics / "start_ns"))) / 1e9 if (metrics / "test_done_ns").exists() else None,
    "peak_rss_kb_process_group": peak_kb,
    "peak_rss_sample_ns": peak_sample_ns,
    "profile_bytes": profile_bytes,
    "coverage_readable": cover_status == 0,
    "coverage_pct": coverage,
    "cpu": timed(metrics / "test.time") if (metrics / "test.time").exists() else {},
    "vet_cpu": timed(metrics / "vet.time") if (metrics / "vet.time").exists() else {},
}
(run_dir / "record.json").write_text(json.dumps(record, sort_keys=True) + "\n")
print(json.dumps(record, sort_keys=True))
if proc.returncode != 0:
    sys.stderr.write(stderr)
    sys.exit(proc.returncode)
