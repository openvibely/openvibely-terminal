---
kind: openvibely.agent_skill
version: 6
skill:
    key: rebase_task_branch
    name: Prepare Delivery Branch For Fast-Forward Integration
    scope: project
    description: Safely prepare, validate, and explicitly authorize a task or delivery branch for fast-forward integration without disturbing shared worktrees.
---

# Prepare Delivery Branch For Fast-Forward Integration

Use when a task or cross-repository contract must be made integration-ready. Do not modify a shared `main` worktree. Direct publication to a shared target is exceptional and requires explicit user authorization.

## Interpret Integration Instructions

- Under a fast-forward-only instruction, “merge to `main`” means advance the target without a merge commit, for example through a verified non-force `HEAD:refs/heads/main` push. Never use a non-fast-forward merge to make progress.
- A rebase applies only to a clean, isolated task or delivery branch. Never rebase, reset, or otherwise rewrite `main`.
- If `main` already contains the candidate, report its exact ancestry and leave history unchanged. Do not convert an existing fast-forward integration into a merge commit.
- If corrective wording ambiguously combines rebase, merge, and a `main` restriction, preserve the strictest constraint and ask for clarification before changing refs.

## Choose The Delivery Method

1. Identify and record the intended target ref, task-feature ref, expected commits, and worktree roots. Check every involved worktree with `git status --porcelain`; a dirty shared target is a boundary, not a cleanup task.
2. Prove ancestry before rewriting: if the target is already an ancestor of the feature, validate the existing branch; if the feature is already an ancestor of the target, no delivery branch is needed. If histories diverge, use `git rebase <target>` only for a clean task branch that may be rewritten. For an isolated backend dependency or a branch that must retain its original history, create a fresh delivery branch at the target and `git cherry-pick` the required commit(s).
3. Never merge into, reset, check out, or edit the shared target worktree. For cherry-pick delivery, create a separate worktree and branch from the verified target, for example `git worktree add -b <delivery-branch> <delivery-worktree> <target>`, then replay only the required commit set there.
4. On rebase or cherry-pick conflicts, inspect conflict markers, combined diffs, and current target behavior. Resolve semantically, preserving compatible behavior from both sides; stage only resolved paths and continue. For generated output conflicts, first resolve the source template or generator input, regenerate the derived file with the repository command, then stage the result.
5. In migration-based repositories, enumerate migration versions on the refreshed target plus the candidate before validation. A duplicate Goose migration version makes database initialization fail even when the rebase applies cleanly. Renumber the candidate to the next free version, update only migration-specific version assertions or fixtures, and add or retain a regression that traverses the resulting chain.
6. For cross-repository delivery where a local candidate branch contains unrelated commits or the shared checkout is dirty, fetch the live remote target first, create a disposable clean worktree at that target, and cherry-pick or apply only the required feature commit(s). Validate and publish that isolated result; never push the unrelated local `main` history merely because it contains the desired change.

## Verify And Publish

1. Confirm the delivery worktree is clean and the current remote target is an ancestor of its `HEAD`. For a fast-forward candidate, use `git merge-base --is-ancestor <target> HEAD` and `git rev-list --left-right --count <target>...HEAD`; target-only commits must be zero. Compare against the live remote target ref, not merely a possibly stale local branch.
2. Run feature-focused regressions and repository validation in the delivery worktree. For this Go project, use `go build ./...`, `go vet ./...`, `go test ./... -count=1`, a zero-output applicable `gofmt -l` check, and `git diff --check`; add the narrowest relevant race test only for concurrency-sensitive changes or explicit requirements.
3. Recheck the expected exact SHA and clean worktree after validation. Attach and read back exact-HEAD Git-note evidence using the `exact_head_validation_evidence` skill; a note on a parent commit does not validate a later merge, rebase, or cherry-pick commit.
4. Before attempting PR, issue, or merge automation, verify that its repository/ref scope includes the delivery branch and that its causal authorization permits the specific action. A tool bound to the current task worktree cannot publish or open review for an external backend branch. If either boundary is absent, do not substitute an unrelated repository or repeatedly retry; report the branch, target, review URL if known, and the precise stable authorization/tool-scope blocker.
5. By default, push only the delivery branch and verify the remote branch resolves to the validated SHA. If the user explicitly authorizes updating the shared target, use only a non-force fast-forward refspec such as `HEAD:refs/heads/main`; immediately before pushing, re-read the live remote target, require it to be an ancestor of `HEAD`, and re-read the remote after pushing. If it advances during validation or before push, do not overwrite it: rebase onto the new target and rerun exact-HEAD validation.
6. Report the unchanged shared worktree, delivery branch and SHA, ancestry result, validation evidence, remote publication or integration state, and any remaining deployment or review dependency. A published branch is not an integrated or deployed backend contract.

## Safety Checks

- Do not treat a dirty shared checkout as permission to stash, commit, clean, or merge someone else's work.
- Do not use an old local `main` as a delivery base when the remote target has advanced; refresh or verify the remote ref before claiming a one-commit integration candidate.
- Do not claim runtime availability until the backend delivery branch is merged and deployed, unless the explicitly authorized fast-forward has completed and the normal deployment state is independently known.
- Never force-push. A `--force-with-lease` ref update is still a force-push under this policy and is not a substitute for the required non-force fast-forward refspec; if the target changes, rebuild or rebase the candidate and rerun validation instead.
- Treat a causal-authorization rejection as a stable delivery boundary, not a source or validation failure; do not attempt to route it through a different repository's automation.
