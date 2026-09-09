# Agent Project-Scope And Edit Safety

Use this checklist when auditing or changing the `/agents` workflow in `internal/tui/`.

## Project Scope

- Agent definitions are project-bound. Interactive `/agents` list, `/agents generate`, `/agents edit`, and `/agents delete` must require a selected project before parsing refs, opening selectors, requesting data, or confirming deletion. Use the existing local `needProject()` behavior; an empty selection must produce the local actionable error and zero HTTP requests.
- Apply the same protection to the `agent` alias; it must share the guarded dispatch path rather than relying on a separate alias-specific check.
- Keep the guard ordering strict: identify the explicit global `metrics` exception from only the first action token, then call `needProject()` for every other invocation before `splitAction`, normal action parsing, ref parsing, selector setup, confirmation, or copying `m.selectedID`. This prevents malformed or ref-less project-bound commands from producing UI side effects while preserving `/agents metrics` without a project.
- Preserve the intentional exception for `/agents metrics`: it uses global endpoints and should remain usable without a selected project. Do not treat the metrics exception as permission to make agent-definition requests unscoped.
- Carry the selected project through every agent-definition request: list/reference resolution, authoritative detail, lifecycle-association reads, generation, update, refresh, and deletion. A backend fallback project is not an acceptable substitute for the TUI selection.
- Treat project ownership as a response invariant too. When authoritative detail identifies its owning project, reject a definition whose owner differs from the selected project even if a misconfigured backend returned it; do not render or mutate it.

## Safe Agent Editing

- Resolve edit references through the existing list semantics, then fetch the authoritative full agent definition. Never reconstruct an update from lossy list cards or terminal-rendered fields.
- Treat `PUT /agents/:id` as a full replacement contract. Start from the authoritative definition, apply only requested changes, and explicitly round-trip every unchanged core, advanced, policy, scope, and association field accepted by the update decoder. Preserve omitted/empty collections and explicit `false` values rather than relying on backend defaults.
- Verify lifecycle-hook serialization against both the lifecycle GET shape and the PUT decoder before resubmitting it. If GET returns fields the update decoder does not accept, do not coerce or partially rebuild the association: use the backend-supported omission behavior that preserves the existing lifecycle set. Still require the selected-project lifecycle read to succeed before mutation when it is part of the authoritative edit contract, and retain the lifecycle data in JSON output.
- Reject protected agents after authoritative detail is loaded and before PUT. Make malformed option syntax, duplicate/missing values, invalid booleans/enums, unknown or ambiguous references, detail failures, lifecycle-read failures, foreign ownership, and protected status produce no mutation request.
- Support explicit empty strings and false booleans as real edits. Exercise quoting through the actual interactive tokenizer and headless argument path so `""` reaches serialization as an empty value rather than being mistaken for omission.
- A successful PUT is the mutation commit point. If the post-save list refresh fails, report a successful partial result using the authoritative updated definition rather than claiming the save failed or retrying the mutation.
- Return the updated authoritative definition in headless JSON mode with stable tagged field names. Keep plain output, static help, subcommand completion, selector behavior, README examples, and accepted edit options synchronized.
- Keep project-generation or epoch guards around asynchronous edit results. A delayed save or refresh from project A must not alter model state or render output after the user switches to project B.

## Regression Coverage

- Test a no-project matrix for list, generate, edit, and delete, asserting the local error, zero requests, and inactive selector/confirmation state. Include a no-project metrics case proving the global exception remains intact.
- Add selected-project cases asserting `project_id` on list, detail, lifecycle, PUT, refresh, and delete requests. Include a foreign-owner detail response and assert no write.
- Decode the raw PUT body/form in client tests and compare preserved empty slices, false booleans, advanced fields, policies, associations, and scope metadata. Explicitly test lifecycle omission when omission is the only lossless preserve-existing behavior.
- Cover protected agents, detail/lifecycle failures, malformed and duplicate options, ambiguous/unknown refs, quoted empty values, and refresh-after-save failure. Assert exact request counts and no PUT on every precondition failure.
- Cover interactive selector exposure, command dispatch, one-shot plain and `--json` output, help/completion/README alignment, and delayed-result generation isolation after project switching.
- Include a dispatch-during-startup test with `/api/projects` intentionally delayed. A project-bound agent command issued while the initial project load is pending must still fail locally without an agent request, selector, confirmation, or lingering `busy` state; the eventual project-load response must not retroactively dispatch the command.
- Relevant audit points are the agent command branches in `internal/tui/registry.go`, command metadata/completion, CLI dispatch, and agent client methods in `internal/client/resources.go`. Finish implementation changes with focused tests, `gofmt`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`. When concurrency paths change, run the narrowest relevant package or tests under `-race`; expand to `./...` only when behavior crosses package boundaries or the task explicitly requires it.
