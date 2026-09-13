# Global Capacity Unlimited Status

Use this guidance when changing `Model.renderStatus`, one-shot `status`, or any terminal view that presents global worker capacity.

- The backend/client contract uses `GlobalCapacity.max_workers == 0` as the unlimited sentinel. `available_slots == 0` is expected in this mode and must not be rendered as literal `0 free`; `has_capacity` remains the authoritative availability signal.
- Match `/workers` terminology by rendering unlimited global capacity as `N running / Unlimited, Q queued`. Omit the maximum and free-slot numbers in the unlimited branch because both are non-semantic there. Keep the finite string and field order unchanged: `N running / M max, Q queued, F free`.
- Centralize the global-capacity status formatting in a narrow helper or shared formatter rather than duplicating the sentinel check across interactive and CLI paths. Interactive `/status` and one-shot `status` should therefore be covered by a parity regression using the same payload.
- Test at minimum empty unlimited capacity, unlimited capacity with nonzero running and queued work, finite capacity compatibility, and `/workers` terminology parity. Assert that unlimited output contains `Unlimited` and never contains `0 max` or `0 free`, while finite output retains all four numeric values.
- Preserve status error, authentication, offline, unhealthy, project-discovery partial-failure, and multi-project behavior; this change is presentation-only. Keep output plain-text and terminal-safe for scripted status consumers.
- After focused renderer/CLI tests, run `go build ./...`, `go vet ./...`, `go test ./... -count=1`, formatting checks, and `git diff --check`.
