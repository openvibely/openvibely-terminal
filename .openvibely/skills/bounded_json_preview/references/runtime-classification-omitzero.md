# Runtime Classification And `omitzero` Pitfalls

Use this note when implementing or auditing bounded JSON preview late-error classification.

## Recurse Through Runtime Maps

A value-sensitive may-fail classifier must handle maps as runtime containers, not fall back immediately to static element-type analysis. Static analysis conservatively marks types such as `map[string]float64` as error-capable because some values may contain `NaN` or infinities. Applied to each value in an outer map, that can falsely exhaust a bounded validation-candidate cap even when every nested map is nil or contains only finite floats.

For non-nil runtime maps:

- Validate the declared key type exactly as `encoding/json` does.
- Track active map identity to detect cycles.
- Inspect actual key conversion risks and actual map values with the correct non-addressable semantics.
- Treat nil maps as safe `null` values.
- Keep traversal and retained validation state bounded; if exact canonical late-error ordering cannot be established within bounds, fail safely rather than guessing.

Differential regressions should include concrete `map[string]any` and reflected outer maps with more entries than the validation cap. Each outer value should contain a nil or finite `map[string]float64`; the preview must match truncated `json.Marshal`. Pair this with a late nested non-finite value and a cycle so value sensitivity does not hide real failures.

## Invoke `IsZero` Once In Canonical Order

Do not call a custom `IsZero` method once during unordered map-value classification and again during rendering. `encoding/json` evaluates `omitzero` while encoding the field, so a stateful, counting, panicking, or order-sensitive `IsZero` method is observable. Preclassification can therefore change output, invoke methods twice, or invoke them in randomized map iteration order.

Before classifying an `omitzero` field as requiring custom code, reproduce `encoding/json`'s nil short-circuits using the actual value:

- A nil pointer whose pointer type implements `IsZero` is zero without a method call.
- A nil interface whose interface type implements `IsZero` is zero without a method call.
- Such an interface holding a nil pointer is also zero without a method call.

These fields must not consume late-validation candidates. Type-only detection is too conservative and can make more than 512 otherwise valid outer-map values return unavailable. Non-nil method-bearing values still require an ordered candidate because their result and side effects are observable.

Design the bounded plan so each custom `IsZero` call occurs once, at the same canonical value-encoding position as `encoding/json`, and reuse that decision for omission and late-error handling. Pure `IsZero` fixtures are insufficient to prove compatibility.

Add regressions with custom value- and pointer-receiver `IsZero` methods that:

- Count calls and assert exactly one call per encoded non-nil field.
- Record ordering across unsorted map insertion/iteration and assert canonical encoded-key order.
- Return different values on successive calls, proving the preview matches the single-call `encoding/json` result.
- Omit a field whose marshaler would fail if reached, plus a non-omitted late control that must still produce the unavailable fallback.
- Exercise over-cap concrete and reflected outer maps containing nil pointer fields, nil method-bearing interfaces, and interfaces holding nil pointers; assert preview parity and zero calls.
