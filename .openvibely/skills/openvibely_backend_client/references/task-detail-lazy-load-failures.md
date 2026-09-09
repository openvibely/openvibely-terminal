# Task-Detail Lazy-Load Failure Propagation

Use this reference when changing task-detail fan-out or the client contract consumed by `/tasks show`.

- Keep the initial task page, thread, changes, and optional lifecycle requests independent and concurrent. Have each goroutine write only its own result and error, wait for all requests, then merge deterministically.
- Do not discard lazy-request failures or convert them into zero-value fragments. Preserve successful sibling sections and return the populated partial detail together with a typed aggregate error keyed by canonical section/tab.
- Make the aggregate unwrap or expose underlying errors so existing `errors.Is`/`errors.As` authentication and transport classification still works. Exact `401` and parsed `/login` redirects must remain authentication errors; network and decode failures must remain distinguishable transport/response errors.
- Keep a successful empty fragment (`2xx` with an empty collection) error-free and visibly different from a failed request or failed decode.
- Preserve public method compatibility where callers depend on existing signatures. Add typed partial-load information without forcing unrelated callers to change behavior, and let commands that require complete context retain their stricter contract when appropriate.
- Regression coverage should combine one failed lazy section with successful siblings, exercise failed thread/changes/lifecycle loads, cover auth and transport classification, verify full-detail and single-tab consumers, and prove valid empty sections still render as empty rather than failed.
