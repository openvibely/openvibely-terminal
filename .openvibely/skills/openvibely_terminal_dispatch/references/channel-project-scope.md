# Channel Management Workflow

Use this reference when auditing or changing `/channels` client and TUI behavior.

## Supported Surface Discovery

- Inventory channel types and capabilities from backend route registration, handlers, Swagger, and web templates before exposing commands. Do not infer support from the existing TUI registry alone. The established backend surface includes GitHub, Slack, Telegram, Discord, and Email; GitHub has no test route, while GitHub and Slack offer browser OAuth connect flows.
- Match browser mutations exactly, including method, route, form fields, expected status, and `project_id`. Preserve Slack removal/disconnect mapping to `/channels/slack/disconnect`; do not derive it from a generic remove route.
- Keep unsupported operations undiscoverable and reject them locally before HTTP. Completion, help, README examples, registry metadata, interactive dispatch, and headless dispatch must describe the same capability matrix.

## Safe Client Boundary

- Parse channel pages into a structured allowlisted model. Expose only canonical identity, type, recognized connection/configuration state, and explicitly safe metadata. Never render whole-card prose, browser action labels, `data-search-text`, hidden form values, test response bodies, or arbitrary backend text.
- Treat backend markup as secret-bearing even when credentials are stored in HTML attributes or form values. Stored secrets may be consumed privately inside the client only when required for an authoritative write; they must not appear in public structs, editable defaults, JSON, errors, logs, transcript, history, or status output.
- Perform authoritative read-modify-write inside the client rather than returning a secret-bearing form snapshot to TUI callers. Preserve omitted stored secrets/settings where the backend supports blank-secret preservation or requires complete replacement forms.
- Classify connection/configuration state with a fixed recognized-state mapping. Check negative phrases such as `not configured` and `not connected` before positive substring matches, and preserve integration-specific distinctions where the backend uses similar badge text for different states.
- Mutation and test errors may preserve safe transport/authentication classification and HTTP status, but must discard backend response bodies because servers can reflect submitted credentials or unsafe text.
- Build browser connect URLs from the configured server URL plus the verified OAuth route, then strip URL user-info, query, and fragment before displaying them. Never echo a credential-bearing configured base URL.

## Dispatch And Interaction

- `/channels` list output should contain only identity, type, connection/configuration state, and safe metadata. Do not print inert `Delete`, `Edit`, `Test connection`, or other web controls as terminal actions.
- Expose discoverable `list`, `show`, `add`/configure, `connect`, `edit`, `test`, `remove`, and `disconnect` actions only where supported. Preserve canonical names such as `GitHub` and multiword refs such as `Telegram Bot`.
- Guard every project-scoped channel action before parsing, selector setup, confirmation, or HTTP when `m.selectedID == ""`. Carry the selected `project_id` through reads, configure/update, OAuth connect, test, remove, and disconnect requests.
- For omitted refs in interactive `show`, `edit`, `test`, `remove`, and `disconnect`, open the searchable selector; headless CLI mode must return usage instead. Resolve exact, then unique prefix, then unique substring, and reject ambiguity. Carry or reuse the canonical selected record so selection does not trigger an unnecessary duplicate list request.
- Resolve a destructive target to its canonical channel before showing confirmation. In TUI mode require explicit `yes`; in one-shot CLI mode require `--force`. Cancellation, ambiguity, validation failure, or a backend error must make no mutation and must not print success.
- Interactive add/edit should use a staged Bubble Tea wizard with masked credential fields. `Esc` cancels without mutation. Clear secret state before returning to ordinary command handling.
- Headless add/edit may accept explicit named option/value pairs, but validate the integration-specific allowlist, required fields, enum values, and operand boundaries before any request. Interactive command lines containing secret options must be redacted before transcript/history insertion even though the normal interactive path uses masked prompts.

## Regression Matrix

- Cover all five integrations and every supported action with exact method/path/query/form assertions, including Slack disconnect and non-default project scope.
- Cover list/show plain and JSON output with sentinel credentials and unsafe card/action text; assert all sentinels are absent and empty collections retain `[]` JSON shape.
- Cover selectors, cancellation, multiword canonical refs, ambiguous refs, unknown refs, unsupported actions, malformed/extra operands, and zero-request local validation failures in both interactive and CLI modes.
- Cover masked setup/edit for every secret-bearing integration, password echo mode, transcript/history redaction, authoritative preservation of omitted values, and cancellation without mutation.
- Cover confirmation ordering, `yes` and `--force` gates, GitHub removal, Slack disconnect, test support exclusions, OAuth URL sanitization, authentication classification, transport failures, and body-blind backend errors.
- Reconcile generated help/completion snapshots and README command examples, then run focused client/TUI tests followed by `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Add focused race-detector coverage only if project scoping changes asynchronous model or request behavior.
