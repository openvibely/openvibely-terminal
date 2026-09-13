# Project Deletion Client Contract

Use this reference when implementing or auditing the Go client for terminal project deletion.

## Verified Contract

- Verify the current backend handler before coding. The current browser-compatible route is `DELETE /projects/<id>` with `HX-Request: true`; the default project is protected by the backend and returns a non-success client error. Do not add a client-side default-project rule that could diverge from backend authority.
- Successful HTMX deletion returns `200` and may include `HX-Redirect: /tasks?project_id=<remaining-id>`. Parse that optional project ID as a selection hint only. Missing, malformed, syntactically invalid, or unrelated redirect information must return no hint, never become a deletion error, and never prevent the required catalog refresh. Only use a hint that is later present in the refreshed catalog.
- Test advisory handling on the actual successful HTMX response header, including malformed and unexpected-path `HX-Redirect` values, not only ordinary `Location` redirects. Keep redirect following disabled in fixture clients so status and headers remain observable.
- Capture the canonical ID before dispatch, URL-escape it as one path segment, and send that exact ID. Keep delete on its established form/HTMX transport unless the backend contract changes.
- Classify `401` and a redirect whose parsed URL path is exactly `/login` as authentication failures; keep ordinary unexpected redirects, transport errors, and backend statuses distinguishable.

## Regression Coverage

Assert method, exact escaped path, HTMX header, status handling, optional redirect parsing, blank-ID validation, backend default rejection, exact-login and `401` classification, non-login redirect handling, malformed successful HTMX redirect handling, and response-body cleanup. Pair client tests with terminal tests that prove the hint is accepted only when it names a refreshed remaining project and that refresh/invalidation still happen without a usable hint.
