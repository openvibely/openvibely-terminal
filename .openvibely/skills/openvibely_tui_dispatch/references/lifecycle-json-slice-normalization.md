# Lifecycle JSON Slice Normalization

Use this checklist when changing lifecycle execution/event output in `internal/tui/registry.go`:

- A backend JSON `null` array decodes into a nil Go slice, and `encoding/json` marshals that nil slice back as `null`; a fixture containing `[]` does not exercise the empty-result contract.
- Normalize nil slices through one small package-local generic helper rather than maintaining type-specific copies. Keep execution and event renderers, client calls, routing, project query propagation, selectors, empty plain-text messages, and event ordering separate.
- Add CLI regressions that make the mock backend return `null` for both execution and event arrays and assert JSON `[]`. Also cover non-empty execution/event results and assert existing snake_case fields and ordering.
- Preserve the established plain-text empty messages (`no executions for this task` and `no events for this execution`) while testing JSON and text paths independently.
- Preserve focused lifecycle coverage for null, empty, and non-empty execution/event results in both JSON and text output.
