---
kind: openvibely.agent_skill
version: 1
skill:
    key: rebase_task_branch
    name: Rebase Task Branch For Fast-Forward Integration
    scope: project
    description: Rebase a clean task branch onto main, resolve conflicts semantically, validate the rewritten branch, and leave main unchanged for fast-forward-only integration.
---

Use when a task branch must be updated onto `main` without merging into `main` or creating a merge commit.

1. Confirm the current branch/worktree is the intended task branch and clean. Record the task branch, `main`, `HEAD`, and `git merge-base main HEAD` before rewriting history.
2. Run `git rebase main` from the task branch. Do not check out, reset, merge into, or otherwise move `main`; do not push unless explicitly requested.
3. On conflicts, inspect `git diff --cc`, conflict markers, and nearby current-`main` behavior. Resolve semantically, preserving compatible changes from both sides rather than selecting an entire side by default. Stage only resolved paths, then continue with `GIT_EDITOR=true git rebase --continue` when no commit-message edit is needed.
4. After the rebase, verify the task branch is clean, `git merge-base main HEAD` equals `main`, and `git merge-base --is-ancestor main HEAD` succeeds. Use `git rev-list --left-right --count main...HEAD` to confirm `main` has zero commits absent from the task branch; this establishes fast-forward eligibility without performing the fast-forward.
5. Run repository-appropriate validation. For this Go project, use formatting checks, `git diff --check`, `go build ./...`, `go vet ./...`, and `go test ./... -count=1`. Add the narrowest relevant `go test -race` package or test only when the rebased diff changes concurrency-sensitive behavior or the task explicitly requires it; do not run the repository-wide race suite merely because the branch was rebased.
6. Report the unchanged `main` commit, rewritten task `HEAD`, ahead count, conflict resolution summary, and validation results. State explicitly that no merge into `main` and no force-push were performed.

If the worktree is dirty, the target branch is uncertain, or resolving a conflict requires guessing intended behavior, stop and ask rather than risking unrelated work or silent semantic loss.
