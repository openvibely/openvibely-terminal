# Commit-Bound Validation Evidence

Use this reference after implementing or integrating an `internal/terminal` change when the next lifecycle step is a separate strict read-only audit, or when validation must be demonstrably tied to one audited commit.

1. Treat this as an implementation/validation turn, not an audit. Do not start the next audit in the same turn. Repository build, vet, and test commands may update external Go caches but must not edit tracked source or intentionally modify the worktree.
2. Capture the required commit with `git rev-parse HEAD`, compare it to the expected audited SHA, and show `git status --short` before validation. Stop rather than attributing results to the wrong revision.
3. Before each substantive gate, recheck that `git rev-parse HEAD` still equals the expected SHA. Report the exact SHA with each outcome so results cannot be inferred merely from a later clean tree.
4. Run the standard gates separately: `go build ./...`, `go vet ./...`, relevant focused regressions, and `go test ./... -count=1`. For briefing-command changes, use the test declarations and matrix described in `briefing-command-operand-validation.md` to select focused CLI and interactive tests, then report the focused group explicitly.
5. Run non-writing hygiene checks over tracked inputs: ensure `gofmt -l $(git ls-files '*.go')` is empty, run both `git diff --check` and `git diff --cached --check`, then capture a final empty `git status --short`. Quote `*.go` so the shell does not expand it before Git receives the pathspec.
6. In the implementation-turn report, enumerate each command or focused test group and its concrete pass/fail outcome, name the exact covered SHA, and state final workspace status. This report is validation evidence for the later audit; do not claim that a fresh audit was completed.

A later audit remains inspection-only: it reviews these recorded outcomes and verifies that their SHA is the audited `HEAD`; it does not rerun validation to fill a missing record.
