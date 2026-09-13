# JSON RawMessage Normalization In Live Events

Use this note when a live-event formatter emits a parsed JSON payload inside a final `encoding/json`-marshaled envelope.

- Trace every complete-payload operation separately: initial unmarshal/field extraction, explicit `json.Compact` or re-marshal, and final envelope encoding.
- Verify the concrete value type. `encoding/json` validates and compacts a valid `json.RawMessage` when it performs the final marshal, so an earlier compaction of that unchanged raw payload can be a redundant full-payload scan and allocation.
- Do not assume removal is safe from call shape alone. Benchmark the current and candidate JSON paths at representative 1 KiB, 64 KiB, and 1 MiB payloads with the same valid-event inputs; report median `ns/op`, allocations/op, and allocated bytes/op.
- Treat numeric acceptance criteria as per-workload gates, not aggregate or best-case targets. Enumerate every required event shape and size, such as recognized, unknown, chat, and task at 1 KiB, 64 KiB, and 1 MiB, and compare each median with its applicable time, allocation, and small-input regression threshold. A miss in one mandatory production-shaped case remains a material acceptance gap even when other cases pass or the overall result is favorable.
- Compare byte-for-byte JSON-mode output for representative nested and whitespace-heavy valid payloads. Retain current behavior for malformed payloads, filtered events, unknown event types, and completion/status records.
- File the opportunity only when the duplicate pass is proven in the current formatter and matched measurements show a material reduction at plausible stream volumes without a small-payload regression.
