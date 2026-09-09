# Malformed Server URL Classification

Use this reference when auditing or changing startup health checks, backend reachability classification, configured-server handling, or offline recovery messaging.

## Open Finding

Bug notification `645264f2a6500639ebbcd722823b553e` reports that a malformed configured server URL can fail while constructing the HTTP request, before any network connection is attempted, but the client classifies the error as proof that the backend responded. A user can therefore see “backend responded but is unhealthy” plus backend-log advice for a percent-escape typo or similar invalid `-server`/`OPENVIBELY_SERVER_URL` value even though no server was contacted.

Before filing related work, page through `list_existing_automation_notifications` and inspect this alert with `get_alert` when overlap is plausible. Recheck current code and tests because the notification may have been implemented or superseded.

## Execution Guidance

- Separate request URL parsing/construction failures from errors produced after receiving an HTTP response. A construction failure does not establish backend reachability.
- Classify malformed configured URLs as invalid configuration or a transport/offline-style failure, with guidance to correct `-server` or `OPENVIBELY_SERVER_URL`; do not label the backend reachable/unhealthy or advise inspecting backend logs unless a response was actually received.
- Keep authentication classification higher priority for real `401` and exact-path `/login` responses, and retain reachable-but-unhealthy classification for genuine HTTP status, API, or response-decode failures.
- Prefer a typed error or explicit phase/cause test over message matching. Preserve safe diagnostics without echoing credentials, cookies, response bodies, or other secrets.

## Regression Coverage

Add focused tests proving that malformed percent escapes and other request-construction failures make zero network requests and produce invalid-server/configuration guidance. Contrast them with connection refusal or timeout, real non-authentication HTTP errors, malformed successful-response payloads, `401`, exact `/login` redirects, and a healthy response. Verify the model/startup surface and backend-required CLI surface do not claim a malformed URL received a backend response.
