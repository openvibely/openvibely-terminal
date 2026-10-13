# Selected project capacity status measurement

`/status` now requests only `/api/capacity/projects/{projectId}` for the selected project. The backend checkout already registers this detail endpoint and returns one `ProjectCapacity` object; it returns 404 when the project does not exist. The TUI collection API remains available for worker capacity views.

## Controlled comparison

Run with:

```sh
OPENVIBELY_STATUS_PERF_EVIDENCE=1 go test ./internal/terminal -run '^TestStatusPerformanceEvidence$' -count=1 -v
```

The harness runs 20 samples per request path and project count. It executes the application's status snapshot loader, including alert and task counts in parallel with capacity loading. The test server adds a fixed 10 ms delay and models 2 MB/s response bandwidth. Decode allocations are measured separately with `testing.AllocsPerRun` over JSON decoding and, for the full path, selected-project lookup.

| Projects | Full p50 | Scoped p50 | Full p95 | Scoped p95 | Full capacity bytes | Scoped capacity bytes | Full decode allocs | Scoped decode allocs |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 11.810 ms | 11.861 ms | 12.251 ms | 12.201 ms | 101 | 99 | 5 | 4 |
| 100 | 17.722 ms | 11.533 ms | 18.922 ms | 12.245 ms | 10,793 | 99 | 210 | 4 |
| 1,000 | 68.754 ms | 12.234 ms | 69.203 ms | 12.299 ms | 107,993 | 99 | 2,023 | 4 |
| 5,000 | 286.640 ms | 11.475 ms | 290.258 ms | 12.237 ms | 539,993 | 99 | 10,049 | 4 |

At 1,000 projects, the scoped request reduced capacity bytes by 99.91% and decode allocations by 99.80%. At 5,000 projects, status snapshot p95 fell by 95.78%. At one project, scoped p95 was 0.41% lower than the full collection result, within the 5% limit. Results are from the controlled local harness and will vary slightly across machines.
