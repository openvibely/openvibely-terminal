# `/skills always|load` State-Setting Regression

Use this check when auditing or changing the `/skills always` and `/skills load` command paths.

These commands promise to enable persistent loading, so they are idempotent state-setting operations: the client/backend request must carry an explicit `true` value regardless of the skill's current always-loaded state. Do not derive the payload by negating the current flag. If an already-enabled skill is loaded again, it must remain enabled rather than being silently disabled. Verify both interactive and headless/CLI dispatch paths, because the same inversion can affect both handlers.

Regression coverage must include at least:

- false current state -> request `true` and persisted enabled state;
- true current state -> request still `true` and persisted enabled state;
- both `/skills always` and its `/skills load` alias, if both are exposed;
- request shape and resulting user-visible success/error behavior.

Run the normal repository validation after implementation: `go build ./...`, `go vet ./...`, and `go test ./...`.
