# Schedule Repeat Normalization Audit

Use this reference when auditing `/schedule add` or related schedule creation paths for duplicated input normalization across the TUI registry and backend client.

## Audit Rule

Trace each public repeat alias from command parsing through the client request payload. A public alias and its backend wire value are one normalization responsibility. If both `internal/tui/registry.go` and `internal/client/resources.go` independently translate the same alias, such as `hourly` to backend `hours`, treat it as maintenance redundancy even when both paths currently emit the correct request. Do not confuse this with a behavior defect, and do not count test expectations or static metadata as duplicate production logic.

## Safe Consolidation

Keep one canonical alias-to-wire mapping at the narrowest boundary that owns the contract, remove the second translation, and preserve the existing backend value and recurrence behavior. Avoid introducing a general command or scheduling abstraction for a single alias table. Verify the client contract before choosing which layer owns the mapping; the companion backend-client guidance owns current route and payload details.

## Regression Checks

Cover the alias through the real TUI dispatch path and the client API path. Assert that `/schedule add ... hourly` still sends exactly `repeat_type=hours`, canonical repeat values remain unchanged, valid intervals retain their existing form, and invalid intervals still cause no schedule mutation. For an implementation, format changed Go files and run focused TUI tests followed by `go build ./...`, `go vet ./...`, and `go test ./...`.

## Notification Audit

For a read-only redundancy run, distinguish this maintenance duplication from an already-fixed hourly behavior item. Paginate the automation-notification inventory, inspect a candidate alert body when its scope may overlap, and file only the narrower uncovered responsibility; adjacent generic action/reload findings do not automatically cover alias normalization.
