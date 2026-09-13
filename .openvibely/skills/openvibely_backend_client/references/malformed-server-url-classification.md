# Malformed Server URL Classification

Use this reference when auditing or changing startup health checks, backend reachability classification, configured-server handling, or offline recovery messaging.

## Execution Guidance

- Separate request URL parsing/construction failures from errors produced after receiving an HTTP response. A construction failure does not establish backend reachability.
- Treat malformed configured URLs as a distinct invalid-configuration state, with guidance to correct `-server` or `OPENVIBELY_SERVER_URL`. Do not label the backend reachable/unhealthy, classify the error as ordinary offline transport, or advise local startup when the configured endpoint is remote.
- Prefer a typed invalid-server-URL error or explicit phase/cause test over message matching. Preserve the classification through model, status/header/hint, configured-login, one-shot CLI, and setup/read-only paths with shared formatters rather than duplicated string checks.
- Validate at the shared request-construction boundary used by every transport path, including JSON, HTML, mutation, and SSE requests. Keeping `Client.New` usable while returning the typed error when a request is built preserves backend-independent help and read-only setup; do not let another endpoint reclassify the same malformed URL as offline.
- Keep authentication classification higher priority for real `401` and exact-path `/login` responses, and retain reachable-but-unhealthy classification for genuine HTTP status, API, or response-decode failures. Preserve valid remote connection-refusal guidance and valid local refusal/setup guidance unchanged.
- Sanitize diagnostics before rendering them. Do not echo URL userinfo, credentials, query values, fragments, terminal controls, cookies, response bodies, or other secrets; prefer a short safe URL/configuration description.

## Regression Coverage

Add focused tests proving that malformed percent escapes and another invalid URL form fail before any network request and produce invalid-server/configuration guidance. Cover both `-server` and `OPENVIBELY_SERVER_URL` through interactive and one-shot CLI paths, including configured-login and read-only setup where applicable. Contrast invalid configuration with connection refusal or timeout, real non-authentication HTTP errors, malformed successful-response payloads, `401`, exact `/login` redirects, and a healthy response. Verify the model/startup surface and backend-required CLI surface do not claim a malformed URL received a backend response or recommend local startup for a remote configured URL.

For transport-focused Go client changes, finish with `gofmt -l`, `go build ./...`, `go test ./... -count=1`, `go vet ./...`, and `git diff --check`; resolve any output from formatting or whitespace checks. Use a freshly built CLI binary for a representative malformed-URL smoke test when runtime behavior changes.
