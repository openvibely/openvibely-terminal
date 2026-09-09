# Project Creation Redirect Contract

Use this note when auditing or changing `CreateProject` and the shared HTML/HTMX mutation transport.

## Contract

Do not apply the generic "all form mutations accept only 2xx" rule to project creation without first checking the endpoint contract. The project-creation workflow may legitimately return a non-login redirect carrying the created project ID, for example:

```text
302 Location: /tasks?project_id=<created-project-id>
```

The client must inspect the response before a shared helper turns every `3xx` into `unexpected redirect status`. It should accept and parse the documented project-creation `Location` response, return the created project, and preserve the existing authentication and error behavior. Exact `/login` redirects and `401` remain authentication-required; malformed, missing, or unrelated redirects remain errors unless the current backend contract explicitly documents them as success. Keep redirect following disabled so the client can classify the original response.

When changing a shared mutation helper, preserve the endpoint-specific distinction rather than broadly accepting redirects for every mutation. Verify the actual backend handler/template or current fixture contract before generalizing.

## Regression

Add a client-level `httptest` regression for `POST /projects` returning `302` with `Location: /tasks?project_id=created-by-location`; assert that `CreateProject` returns the project ID and no error. Also retain coverage for the normal successful response, exact login redirect, `401`, and unrelated or malformed `3xx` responses. Custom fixture clients must set `CheckRedirect` to `http.ErrUseLastResponse`.

A malformed absolute `Location` can be rejected by Go's HTTP client while it is parsing the response, before `CreateProject` receives the response and its project-ID parser runs. To exercise client-side malformed-ID validation, use a syntactically valid redirect target with an invalid query escape such as `Location: /tasks?project_id=%zz`; assert a clear creation error and that no project is returned.

For an ad hoc reproduction that imports `internal/...`, place the temporary harness inside the module/repository boundary; a harness outside it is rejected by Go's `internal` package visibility rule. Remove the harness afterward and verify the worktree is clean.
