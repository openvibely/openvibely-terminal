# Project-Reference Ranking Consolidation

Use this guidance when two project-edit or project-selection paths independently rank the same references but intentionally differ after the shared tiers.

- Extract only the common base-tier classification into a narrow pure helper: exact ID is tier `0`, exact name is tier `1`, and ID/name-prefix matches are tier `2`.
- Keep path-specific wrappers responsible for their different unmatched behavior. A successful-candidate path may retain its fallback tier `3`; a failed-boundary diagnostic path may retain name-substring tier `3` and a distinct no-match sentinel. Do not collapse those semantics into the shared helper.
- Do not replace the command parser or change generic `matchRef` behavior when the maintenance target is only duplicated project-ranking conditions. Preserve cross-boundary arbitration, equal-exact-name ambiguity, option-like names, literal-pipe handling, and the no-request-on-ambiguity contract.
- Add table-driven helper coverage for exact ID, exact name, ID-prefix, name-prefix, substring-only error selection, no match, and successful fallback. Retain interactive and headless project-edit tests for resolution parity, ambiguity, and zero mutation requests on ambiguity.
- After the final test edit, run `gofmt` on changed Go files, `go build ./...`, focused `internal/terminal` regressions, uncached `go test ./... -count=1`, `go vet ./...`, and `git diff --check`; inspect status and the final diff to confirm the change stayed within the ranking helpers and tests.
