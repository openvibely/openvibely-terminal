---
kind: openvibely.agent_skill
version: 20
skill:
    key: bounded_json_preview
    name: Bounded JSON Preview Engineering
    scope: project
    description: Implement and validate bounded deterministic JSON-like previews while preserving encoding/json semantics and controlling allocation.
---

# Bounded JSON Preview Engineering

Use for lifecycle/event rendering or other paths that must show a deterministic bounded prefix of arbitrary Go values without fully materializing encoded output.

## Procedure

1. Treat `encoding/json` as the semantic oracle. Differential-test ordinary fixtures with `json.Marshal` followed by the same display truncation used by the preview.
2. Bound output retention independently from error validation. After the visible prefix fills, continue only work needed to detect unsupported values, non-finite floats, invalid `json.Number`, excessive depth, cycles, failing marshalers, and ordering uncertainty. These must return the unavailable fallback rather than a plausible but invalid prefix.
3. Track active map, slice, and pointer identities on the current recursion path and remove each identity on return. Slice identity must include length so valid shared-backing subslices are not mistaken for cycles. Count JSON container depth separately from pointer/interface indirection.
4. Invoke each `json.Marshaler` or `encoding.TextMarshaler` at most once per preview. Validate complete custom JSON output, but retain and compact only its bounded visible prefix. Copy retained TextMarshaler map-key bytes immediately because implementations may reuse mutable scratch storage.
5. Implement struct field planning before rendering or classification. Preserve anonymous promotion, least-depth/tagged dominance, valid tag names, `json:"-"`, promoted fields through unexported anonymous structs, nil embedded pointers, `omitempty`, `omitzero`, and the active Go toolchain's `,string` behavior. Match actual addressability: map values are non-addressable, pointer dereferences and slice elements can be addressable, and pointer-receiver methods must dispatch only where `encoding/json` would dispatch them.
6. Traverse arrays, slices, maps, pointers, interfaces, and selected struct fields using runtime values. Finite floats, nil maps, nil slices, and nil marshaler pointers are safe. Nil pointer JSON/Text marshalers encode as `null` without invocation. Repeated safe sibling types are not recursion; static/type recursion state must be path-local.
7. For maps, validate the declared key type before entries. Accept only string, integer, or declared `encoding.TextMarshaler` key types; a runtime key in `map[any]...` does not make the declared type valid. Preserve canonical encoded-key ordering. Use one `MapRange` pass, retain selected values directly, and avoid `MapKeys`, repeated scans, and selected-value lookups.
8. Marshal reflected TextMarshaler keys once during the initial map pass. A nil pointer TextMarshaler key becomes the empty key without invocation. Keep a bounded smallest-key set for visible output and a separate bounded sorted set of values requiring late validation. Apply the same candidate bound to concrete fast paths such as `map[string]any`.
9. Classify map values value-sensitively before adding late-validation candidates. Recurse through nested maps as well as pointers, arrays, slices, interfaces, and selected struct fields. For nested maps, validate their declared key type, treat nil maps as safe, inspect actual values, and use active map identities to detect cycles. This allows more than the candidate cap of finite or nil nested maps while still detecting a late nested NaN, unsupported key/value, marshaler failure, or cycle.
10. Never invoke a custom `IsZero` method during unsorted may-fail classification. `IsZero` is user code whose result and side effects are observable. Conservatively classify a non-nil method-using `omitzero` value as requiring ordered validation, then invoke `IsZero` exactly once during canonical-order rendering/validation. Match `encoding/json`'s runtime nil short-circuits: nil pointers implementing `IsZero`, nil method-bearing interfaces, and method-bearing interfaces containing typed nil pointers are omitted without invoking `IsZero` and must not consume late-validation candidates. Built-in non-method zero checks may be evaluated during classification. If more than the bounded candidate count requires custom `IsZero`, fail safely before invoking any such methods rather than calling them in map iteration order or twice.
11. Preserve canonical map-value method invocation order after the visible prefix fills. Exact duplicate encoded names and truncated-name prefix relationships require care: do not discard equal-name late candidates using only the last visible key and strict less-than. When one complete key equals a truncated key's retained prefix, the complete key sorts first.
12. Bound aggregate retained key bytes, key-comparison work, and late-validation candidate count. If discarded suffixes, collisions, or overflow make exact ordering unknowable, return unavailable rather than guessing or retaining payload-sized data. As soon as a key comparison establishes ambiguity, do not insert or sort the triggering key: a comparator must not fall through from equal retained prefixes to comparing unrestricted source strings. Once fallback is certain, still finish the single key-marshaling pass to preserve key call cardinality and surface key errors, but retain no additional key output.
13. Do not reject every oversized key automatically. One oversized key, or multiple keys whose retained prefixes establish exact relative order, can remain previewable even with successful error-capable values. Ambiguity must come from actual key comparisons.
14. Do not let ignored, omitted, or shadowed fields consume output or validation budget. Confirm any claimed compatibility defect with a minimal differential regression before changing traversal.

## Regression Coverage

Cover ordinary/unexported/ignored fields, anonymous promotion and conflicts, tagged dominance, nil embedded pointers, `omitempty`, `omitzero`, multiply indirect `,string`, nested custom marshalers, addressable versus non-addressable pointer methods, wide arrays/slices/maps, invalid values after the prefix, and true cycles. Compare output to `json.Marshal` plus display truncation.

For map bounds, test both reflected maps and the concrete `map[string]any` path above the candidate cap. Include typed nil JSON/Text marshaler pointers, non-addressable map values with pointer-only methods, and addressable slice controls. Include oversized and colliding TextMarshaler keys, reused scratch buffers, nil pointer keys, unsupported declared key types, and stateful/order-sensitive values. Add reflected native-string keys with arbitrarily long common prefixes and assert the ambiguity fallback occurs without any full-source string comparison after the retained prefix is exhausted.

For nested map classification, add over-cap concrete and reflected outer maps containing finite nested maps and nil nested maps; they must remain previewable. Pair them with a late nested non-finite float and a nested map cycle, and with unsupported or failing nested values when applicable, to prove runtime traversal still detects real errors.

For custom `IsZero`, use a stateful type with no marshaler method of its own. Compare the call log and output against `encoding/json`; assert one call per non-nil field in canonical map-key order. Add over-cap concrete and reflected outer-map fixtures containing nil `IsZero` pointers, nil method-bearing interfaces, and interfaces holding typed nil pointers; they must remain previewable and invoke `IsZero` zero times. At the candidate limit, omission and a late nonzero/error control must behave correctly. Above the limit, assert unavailable is returned before any non-nil `IsZero` invocation.

For recursive type analysis, include acyclic sibling fields of the same safe type in arrays, slices, and over-cap reflected maps. They must not be classified as cycles merely because a type repeats.

## Benchmark And Validation

Benchmark 1 KiB, 64 KiB, and 1 MiB fixtures with one and many events. Separate payload-byte, map-cardinality, key-length growth, and common-prefix comparison work. Include decoded wide maps, wide error-capable maps, wide value-sensitive nested maps, reflected native-string maps with ambiguous long common-prefix keys, and fixed-high-cardinality oversized TextMarshaler keys. Use `-benchmem`; ensure bounded retention and comparison latency do not scale as key count multiplied by key size, and investigate unexpectedly payload-linear allocation or runtime before claiming bounded performance.

Run `gofmt` on changed Go files, targeted regressions repeatedly, focused benchmarks with `-benchmem`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `git diff --check`, and final worktree/status checks. Deliberate duplicate JSON-tag fixtures can trigger `go vet`; model conflicts through embedded types instead.
