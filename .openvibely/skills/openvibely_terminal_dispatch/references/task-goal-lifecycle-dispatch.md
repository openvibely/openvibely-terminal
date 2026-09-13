# Task Goal Lifecycle Dispatch

Use this note when extending `tasks goal` in the interactive registry or headless CLI.

## Grammar And Resolution

- Register explicit nested actions `tasks goal pause <task>` and `tasks goal resume <task>` across dispatch, help, completion, and README/user-guide examples. Resolve the task through the selected project's task set before issuing any mutation.
- Keep the legacy form `tasks goal <task> | <objective>` intact. In particular, parse the pipe form before interpreting action keywords so `tasks goal <task> | pause` remains an objective named `pause`, not a pause lifecycle action.
- Use the established exact, unique-prefix, and unique-substring reference matching. Missing, ambiguous, and foreign references must fail locally before a POST; omitted refs should follow the existing TUI selector path and headless CLI usage behavior.
- Require a selected project before project-scoped goal reads or mutations, and carry that ID through every set, clear, pause, and resume call.

## Mutation And Output Safety

- Dispatch pause/resume only after successful reference resolution, and render success only after the client confirms the exact scoped route succeeded. Backend errors must remain visible and must not append a success message.
- Add focused tests for interactive dispatch and `RunCLI`, exact method/path/query, URL encoding, missing/ambiguous/foreign refs, backend failure output, and the pipe/objective compatibility case.
- Update registry metadata, help, completion, README, and user guide together so generated and written command references describe the complete set/clear/pause/resume lifecycle.

## Validation

Run focused terminal/client tests followed by `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check` from the active TUI worktree. Inspect the final diff and status after the last validation pass.
