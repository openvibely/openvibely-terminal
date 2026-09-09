# Attachment Upload Local-Input Safety

Use this reference when auditing or implementing task attachment uploads from local filesystem paths.

## Safe Open And Validation

- Validate every attachment before multipart construction and before any preliminary or upload HTTP request. Body-reader or lazy-stream errors are too late to guarantee zero backend requests.
- On every FIFO-capable target supported by `golang.org/x/sys/unix`, open the target with `O_RDONLY|O_NONBLOCK|O_CLOEXEC`, then inspect the opened descriptor with `Stat`. Reject every descriptor whose mode is not regular, and close every validation descriptor on both success and rejection.
- Treat platform build constraints as part of the safety boundary. Keep AIX and illumos on the nonblocking Unix opener, not the path-stat fallback; explicitly reconcile positive and negated tags so exactly one opener builds per target. Remember that illumos may inherit the `solaris` tag, but keep direct target compile coverage rather than relying on that implication.
- Descriptor inspection, rather than path-only metadata, is required to resist replacement races. Keep descriptor identity/size verification in the later lazy upload open as well when uploads are streamed.
- Preserve supported symlink-to-regular behavior by following the symlink during open and validating the resulting descriptor. Never accept a special target merely because path metadata looked safe earlier.
- Use the path-stat fallback only on targets where the nonblocking Unix opener is unavailable. Preclassification alone is not race-safe on FIFO-capable systems because a validated regular path can be replaced before `os.Open` and block indefinitely.
- Return path-specific errors for missing, inaccessible, replaced, or non-regular inputs. Do not rely on request context cancellation to interrupt a blocking local `open` or special-file read.

## Regression Coverage

- Write the reproducing filesystem regression before production changes.
- Use `golang.org/x/sys/unix` rather than the standard `syscall` package for cross-Unix FIFO/open test primitives; APIs such as `syscall.Mkfifo` are not exposed consistently on AIX.
- On Unix, cover a FIFO with no writer and a connected FIFO; both must reject promptly without blocking on open or read.
- Prepare a regular file and multipart body, replace the path with a FIFO before lazy open, then assert prompt path-specific rejection and that `Close` reports the same terminal failure. A timeout branch may connect the FIFO only to release a buggy blocked goroutine before failing the test.
- Cover available devices or other special files, cancellation, and explicit descriptor closure for rejected inputs.
- Assert rejected local inputs cause zero HTTP requests, including any preliminary attachment-list request.
- Preserve successful symlink-to-regular uploads and ordinary and empty-file multipart filename, content, order, project scope, and response behavior.
- Keep replacement and mid-upload mutation tests to verify descriptor identity and size race handling.
- If legacy tests expect later missing paths to fail only during streaming, update them to the fail-fast contract while retaining independent read/close error-precedence coverage for inputs that passed validation.

## Validation

Run focused filesystem tests first, then compile `internal/client` tests for `GOOS=aix GOARCH=ppc64`, `GOOS=illumos GOARCH=amd64`, and the fallback target such as `GOOS=windows GOARCH=amd64`. Finish with `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Run the narrowest relevant replacement/open tests under `-race` only when the implementation or regression actually exercises concurrent replacement or shared state; do not make an all-package race run the default.

Historical evidence: notification `f5262b0e418510c7ad71e4cad420edbb` identified a task attachment FIFO that blocked before backend access. A later audit found that AIX and illumos still selected the path-stat fallback, allowing a prepared regular file replaced by a FIFO to block during lazy open; the correction moved those targets to the nonblocking descriptor opener and added replacement-race coverage.
