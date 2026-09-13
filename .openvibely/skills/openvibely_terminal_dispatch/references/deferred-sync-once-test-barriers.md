# Deferred `sync.Once` Barriers in Concurrency Tests

When a test must keep a handler blocked until assertions have observed concurrent requests, defer a closure around `sync.Once.Do`; do not defer the `Do` call itself.

Incorrect:

```go
defer releaseOnce.Do(func() { close(releaseGlobal) })
```

The arguments to `defer` are evaluated immediately, so `Do` runs while registering the cleanup and may release the barrier before the test observes the overlap.

Correct:

```go
defer func() {
    releaseOnce.Do(func() { close(releaseGlobal) })
}()
```

Keep the explicit `releaseOnce.Do(...)` after the test has observed the required request-start events. The deferred closure then provides idempotent cleanup on assertion failure or early return without weakening the concurrency assertion. For this class of test, run the focused timing/overlap test under `-race` in addition to the normal build, vet, package, and repository validation.
