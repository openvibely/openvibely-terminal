#!/usr/bin/env python3
import json
from pathlib import Path
import statistics
import sys

root = Path(sys.argv[1])
cap = sys.argv[2]
mode = sys.argv[3]

def p95(values):
    return statistics.quantiles(values, n=20, method="inclusive")[18]

def stats(rows, key):
    values = [row[key] for row in rows]
    return statistics.median(values), p95(values)

print(f"MODE {mode}")
rows = [
    json.loads(path.read_text())
    for path in sorted((root / f"{cap}-{mode}").glob("run-[0-9][0-9]/record.json"))
    if json.loads(path.read_text())["run"] > 0
]
wall = stats(rows, "wrapper_wall_s")
vet = stats(rows, "vet_done_s")
test = stats(rows, "test_done_s")
rss = stats(rows, "peak_rss_kb_process_group")
cpu = [row["cpu"].get("user", 0) + row["cpu"].get("sys", 0) for row in rows]
print(
    f"CAP {cap} n={len(rows)} statuses={sorted(set((r['vet_status'], r['test_status']) for r in rows))} "
    f"wall={wall[0]:.3f}/{wall[1]:.3f}s "
    f"vet_done={vet[0]:.3f}/{vet[1]:.3f}s "
    f"test_done={test[0]:.3f}/{test[1]:.3f}s "
    f"cpu={statistics.median(cpu):.3f}/{p95(cpu):.3f}s "
    f"rss_mib={rss[0]/1024:.1f}/{rss[1]/1024:.1f} "
    f"profiles={sorted(set((r['profile_bytes'], r['coverage_readable'], r['coverage_pct']) for r in rows))}"
)
