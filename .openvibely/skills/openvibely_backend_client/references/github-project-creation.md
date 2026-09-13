# GitHub-Backed Project Creation

Use this reference when extending terminal project onboarding beyond local filesystem repositories.

## Implementation Pattern

- Verify the backend's current `POST /projects` contract and the existing project-creation redirect/identity behavior before coding. Reuse the client’s existing HTML/form transport, cookie handling, redirect policy, and response parsing; do not duplicate cloning, GitHub authentication, or credential logic in the terminal client.
- Preserve the public local creation contract and behavior, including `CreateProject(name, path)` callers and names/paths containing spaces. Add an explicit source-specific method or private request helper for GitHub rather than silently changing the old method's meaning.
- Keep the command grammar explicit: local `projects create <name> <path>` and GitHub `projects create <name> --github-url <url>`. Use the repository's quote-aware parsing path so quoted names remain one operand and URLs retain punctuation such as `:`, `/`, `.`, `-`, `_`, `?`, `&`, and `#`; do not split URLs or infer a repository source from a path-like string.
- When adding `--github-url` or any new option marker to an existing positional grammar, do not treat the marker as an option unconditionally. Enumerate legacy inputs where that token is literal project-name or path text, evaluate the complete local and new-option parses under the same precedence rules, and either preserve the legacy interpretation or reject an actually ambiguous form. Add regressions for the marker as a name, path, and part of a space-delimited operand, including both interactive and headless parsing where supported.
- For GitHub creation, send exactly `repo_source=github` and `repo_url`; omit `repo_path` entirely. Let the backend perform cloning and PAT/authentication. Keep local creation's `repo_source=local` and `repo_path` fields unchanged.
- Classify and render GitHub clone/authentication failures as GitHub/backend failures, not local-path validation errors. Keep diagnostics actionable and terminal-safe: preserve safe status/context and recovery guidance, but redact PATs, tokens, passwords, cookies, authorization headers, and secret-bearing response bodies before plain or JSON output.
- On success, install the backend-assigned project identity and use the same selection and live-event ownership path as local creation. Reconnect or scope SSE with the newly selected `project_id`; stale creation/list messages must not select an older project after a project switch.
- Preserve existing plain and `--json` output schemas and field casing. Add explicit JSON tags to any newly exposed structs and ensure empty collections remain `[]` rather than `null` where that is the established CLI contract.
- Document both forms in generated `help projects`, README, and the user guide. Explain that GitHub mode uses backend-managed cloning/authentication and is the supported alternative when local paths are disabled or unavailable.

## Regression Matrix

Cover both direct client and real command/TUI paths for quoted project names, local names and paths with spaces, punctuation-rich GitHub URLs, exact request fields, omission of `repo_path`, backend clone/PAT failures without credential leakage, local-path-disabled environments, backend-assigned ID installation and selection, SSE query scoping, and plain/JSON output. Retain existing local creation, redirect, authentication, and malformed-response cases.

Add a compatibility-collision matrix for every newly introduced option or separator: the token used as a literal name, literal path, embedded operand text, and valid option marker; complete and incomplete option forms; quoted and unquoted inputs; and any pipe or outer-`--` boundary that reaches the real executable. Assert that legacy valid forms remain valid, ambiguous forms fail without mutation, and no parser fast path shortens or rebinds the project reference.

## Validation

After focused tests, run `go run ./cmd/openvibely-terminal help projects`, `go build ./...`, `go test ./... -count=1`, `go vet ./...`, `gofmt -l` on the repository's Go sources, relevant documentation/help checks, and `git diff --check`. Treat any formatter or whitespace-check output as a failure to resolve.
