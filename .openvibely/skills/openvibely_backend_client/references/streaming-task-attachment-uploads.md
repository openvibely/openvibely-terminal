# Streaming Task Attachment Uploads

Use this reference when changing large multipart task-attachment uploads in the Go client.

## Implementation Pattern

1. Perform the preliminary attachment lookup before opening files or starting any body producer. A failed lookup must cause no local file I/O.
2. Do not serialize payload bytes into a `bytes.Buffer`. Prepare only bounded metadata and multipart headers, then expose a pull-based `io.Reader` that opens and reads parts on HTTP transport demand. Pull-based reading provides natural slow-consumer backpressure and avoids producer goroutines that can remain blocked after cancellation or early server rejection.
3. If the backend or transport needs fixed `Content-Length`, stat each file after the preliminary lookup and compute the exact multipart length from encoded headers, file sizes, separators, and the closing boundary. Never retain all file descriptors or pre-read payload bytes merely to determine length.
4. Preserve one repeated `files` part per selected path, in caller order. Preserve duplicate and empty files, base filenames, multipart escaping, selected-project query, `HX-Request`, content headers, and existing before/after attachment matching.
5. Keep descriptor use independent of selection count: retain stat metadata, lazily open only the current part, and close it before advancing. On success or any early return, close the active handle.
6. Fixed-length streaming must not silently upload stale or truncated content. Retain preflight `os.FileInfo`; after lazy open, stat the opened handle and require both the prepared size and `os.SameFile` identity to match when identity metadata is available. This catches replacement, including same-size replacement, between path stat and open.
7. After exactly the prepared byte count has been read, probe the opened handle for one additional byte before emitting the next multipart boundary. An additional byte means the file grew during upload and must fail as a local changed-file error. EOF permits continuation; a non-EOF probe failure remains a local read error. This preserves fixed `Content-Length` without silently truncating a growing file.
8. Preflight metadata collection must not change ordered local-file failure semantics. If stat discovers a missing, inaccessible, or invalid later path, retain that per-path failure and surface it only when streaming reaches that part; otherwise a later path can preempt an earlier file's open, read, or close failure. Preserve the established public error category and wrapping, including converting a stat-discovered missing/inaccessible path to the prior `open attachment "<path>": open <path>: ...` shape when that is the compatibility contract.
9. Make request-body `Read` and `Close` concurrency-safe. Go's HTTP transport may close a request body concurrently during cancellation or an early response. Synchronize mutable multipart state, detach the active handle before closing it, and make repeated `Close` idempotent so concurrent close cannot race with reads, double-close, or dereference a cleared handle.
10. Idempotent `Close` means completion and result consistency, not merely one underlying close call. If an open or underlying file close is already in progress, every concurrent `Close` must wait for it to finish and return the same recorded terminal error. Use explicit completion barriers for lazy-open cleanup, normal file-close transitions, and body-wide close; this also covers asynchronous `net/http` request-body closure after the transport returns.
11. If `Close` wins while lazy `Open` is blocked, the opener owns cleanup of any handle it eventually receives. It must close that handle, record an underlying close failure before releasing the open-completion barrier, and make both the racing `Read` and all `Close` callers observe that failure rather than EOF or nil.
12. Do not assume context cancellation alone will interrupt a blocked custom body `Read`. Arrange for cancellation to actively close the body and current file, for example with `context.AfterFunc`, and stop/join that callback before normal final cleanup. Record the context cause before closing so cancellation remains the reported error instead of a closed-file or closed-network artifact.
13. Keep local failures distinguishable from downstream request failures. Track the first genuine local open/stat/read/close/change error separately from the body's general terminal error. After transport and body cleanup complete, return that local error directly before a `net/http` request error so the public error retains its exact historical form; cancellation and backend/transport rejection must still remain authoritative when no genuine local failure occurred.

## Regression Coverage

Cover multiple files, duplicates, empty files, exact bytes and order, filename escaping, and fixed `Content-Length`. Add injected open/stat/read/close failures, preliminary-lookup failure before file access, cancellation, timeout, early rejection, connection reset, slow producer, slow consumer, and repeated-run cleanup checks. Retain existing project-scope, HTMX/header, backend-error, partial-outcome, refreshed-fragment parsing, and before/after matching tests.

Add direct concurrency/resource regressions:

- Drive a real `net/http` request whose attachment `Read` blocks until `Close`, cancel its context, and assert prompt completion, one active-handle close, and a stable cancellation error. Repeat this test enough times to expose cancellation/transport races.
- Block lazy `Open`, race body `Close`, then return a handle whose close fails. Assert the reader and every close caller wait for cleanup and satisfy `errors.Is` for the same underlying close failure.
- Start one body `Close` whose underlying file close blocks and eventually fails, then call `Close` concurrently. Assert both callers remain blocked until cleanup completes, both observe the same close error, and the underlying close runs exactly once.
- Simulate a transport that starts request-body closure asynchronously and returns. Assert the upload operation still waits for body cleanup and surfaces the eventual underlying close error.
- Use real files to replace a prepared path with another file of the same size and to grow a file before and during streaming. Assert replacement/growth fails before the next multipart boundary, rather than relying only on size mismatch or accepting a fixed-length truncated upload.
- Require exact equality of public local open/read/close error strings, plus `errors.Is` where applicable. Substring checks alone do not catch accidental `POST ...: Post ...:` wrapping from `net/http`.
- Use real missing/inaccessible paths so stat behavior is not hidden by a global stat stub. Assert the resulting error retains the established open-attachment type/text, then combine a valid first path with a failing later path and inject earlier open, read, and close failures to prove the later preflight failure never wins.
- Upload many instrumented parts and track simultaneously open handles; assert the maximum is one, all handles close, and order, duplicates, and empty parts remain unchanged. Also exercise body-level concurrent `Read`/`Close` under `go test -race` to catch state races and panics deterministically.

## Performance Measurement

Use matched 1, 32, and 128 MiB inputs with at least 10 iterations per size. Compare the candidate against the prior buffered behavior under the same fixture and report allocations/bytes per operation, median first-body-byte latency, and median total upload time measured separately for every iteration. For even sample counts, compute the median from the two center observations rather than selecting only the upper center.

Acceptance checks:

- 128 MiB heap growth is at most 8 MiB above the 1 MiB baseline.
- The first body byte is observed before the complete payload has been read and latency improves at least 80% for the large case.
- The 1 MiB median total time regresses no more than 5%.
- Larger uploads are no slower.

A fixed-size copy buffer alone may satisfy heap bounds while losing throughput through `io.Pipe` synchronization. Unknown-length request bodies may also regress large transfers because Go uses chunked framing. If a 10-iteration large-case timing contradicts prior runs, increase the matched iteration count and report the stable median rather than relying on benchmark mean `ns/op` or one noisy sample.

Finish transport changes with focused repeated tests and benchmarks plus `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Run the focused concurrent `Read`/`Close` and producer/cancellation tests under `-race`; expand to an all-package race run only if the behavior crosses package boundaries or the task explicitly requires it.
