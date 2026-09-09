# Adversarial Map-Bound Checks

Use these checks when reviewing or changing bounded canonical JSON map previews.

## Native String Keys

- A retention cap for `encoding.TextMarshaler` keys does not automatically bound native string keys. Audit concrete `map[string]any` and reflected string-key paths separately.
- Never retain unrestricted native key strings in preview candidates when comparisons can scan their full common prefixes. Store a bounded copied prefix plus complete/truncated metadata, as with marshaled keys.
- Preserve canonical ordering only when retained prefixes prove it. If discarded suffixes can affect visible ordering or late-error invocation order, fail safely rather than comparing or retaining complete payload-sized keys.
- Benchmark adversarial keys that are individually 1 MiB and differ only at the final byte. Use enough keys and repeated runs to expose comparison amplification; a two-key benchmark may show linear scaling without enforcing a useful bound.

## Nested Nil Marshalers

- Top-level nil-pointer detection is insufficient for late-error candidate classification. A struct value may contain selected fields that are nil pointers implementing `json.Marshaler` or `encoding.TextMarshaler`; `encoding/json` emits `null`, or omits them under the applicable tag, without invoking the nil receiver.
- When a bounded candidate cap depends on whether a runtime value can fail, inspect the selected runtime field graph where practical. Do not let type-only analysis classify every nested marshaler pointer as failing when the actual field is nil.
- Add over-cap differential tests for concrete and reflected maps whose values are structs containing nil JSON- and TextMarshaler pointer fields. Cover emitted `null` and `omitempty`/`omitzero` omission, assert receiver call counts remain zero, and require the preview to match `truncate(json.Marshal(value))` rather than return unavailable.
- Pair nil fixtures with non-nil failing controls so value-sensitive classification cannot suppress genuine late failures.
