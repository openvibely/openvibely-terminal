# Safe Agent Editing

Use this guidance when implementing or auditing `/agents edit` in `openvibely-tui`.

## Reference Resolution

- Do not concatenate agent name and key and pass that composite string to generic matching. It destroys exact-name precedence when one name prefixes another.
- Resolve in explicit tiers: exact canonical ID, exact case-insensitive name, exact case-insensitive key, unique prefix across ID/name/key, then unique substring across name/key.
- At every tier, reject multiple matches as ambiguous rather than falling through or choosing the first result.
- Keep the supplied reference and backend fields raw throughout comparison. Sanitize only when constructing user-visible unknown or ambiguity diagnostics so safety changes cannot alter exact/prefix/substring matching semantics.
- Ambiguity diagnostics must sanitize both the supplied reference and every backend-controlled candidate label before quoting, joining, styling, or returning the error. Strip ANSI/OSC and controls, and replace newline, carriage return, and tab with spaces.
- Cover canonical IDs, exact names that prefix another name, exact keys, unique prefixes, unique substrings, and ambiguous prefixes.

## Model Validation

- Treat `inherit` as the only catalog-independent sentinel; accept it case-insensitively and normalize it to lowercase.
- For any other edited model value, load the selected project's configured models and require an exact match against `LLMModel.Model`, not the model card ID or display name.
- Perform model validation after agent reference resolution but before authoritative agent detail/lifecycle reads and before PUT. Invalid values must cause zero detail reads and zero mutations.
- If model catalog loading fails, report validation failure and do not mutate.

## Persisted Output

- A successful PUT does not prove the backend persisted the submitted object unchanged. Backend normalization may alter model, status, timestamps, or other fields.
- In JSON mode, fetch the authoritative agent definition again after PUT and marshal that refreshed definition.
- If the authoritative refresh fails after a successful PUT, preserve mutation success but emit a structured partial result such as `{"saved":true,"refresh_error":"..."}`. Do not include the locally attempted agent fields, because they are not verified persisted state.
- In plain mode, refresh the agent list and derive the success label and table from refreshed records when available. If refresh fails, report a clear saved-but-refresh-failed status without turning the completed mutation into an error.

## Terminal Safety

- Sanitize the plain success label and every dynamic cell in the refreshed agents table independently before formatting or styling.
- Strip ANSI/OSC sequences, control/format characters, and replace newlines, carriage returns, and tabs with spaces for single-line fields.
- JSON mode should remain valid structured JSON and should not be passed through plain-text sanitization.

## Regression And Validation

- Add raw-output tests that inject ANSI CSI, OSC, bell, and newlines into name, scope, model, and description. Inspect the raw result body before any ANSI-stripping helper, then assert readable sanitized text remains.
- For ambiguity errors, test the matcher directly with malicious supplied references and candidate names, then exercise both callers: inspect the interactive `resultMsg` error before Bubble Tea styling and the error returned by `RunCLI` (headless failures may not be duplicated to stdout). Assert no mutation request occurs.
- Use a two-version detail fixture for JSON: return one definition before PUT and a backend-normalized definition after PUT, then assert the output uses only the second version.
- Simulate post-PUT detail failure and assert successful partial JSON with `saved: true`, `refresh_error`, and no unverified agent object.
- Run focused agent-edit tests, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, formatting checks, and `git diff --check`.
