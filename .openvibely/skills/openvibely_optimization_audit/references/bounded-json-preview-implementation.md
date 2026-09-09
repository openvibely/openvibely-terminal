# Bounded JSON-Like Preview Implementation

Use this checklist when implementing a bounded plain-text preview for lifecycle or log payloads while a raw JSON mode must remain complete.

## Compatibility Contract

- Keep ordinary in-limit previews byte-identical to `encoding/json`, including deterministic object-key ordering, HTML-safe escaping, Unicode width behavior, malformed UTF-8 replacement, numeric formatting, named scalar/container types, structs, nil containers, and `[]byte` base64 encoding.
- Preserve `encoding/json` method dispatch. Addressable slice elements can select pointer-receiver `json.Marshaler` or `encoding.TextMarshaler`; recurse with addressable `reflect.Value` elements and preserve addressability through bounded struct clones rather than converting values to interfaces too early.
- Do not classify every slice with an underlying `uint8` element as base64 data. Before using the `[]byte` fast path, check both the named element type and its pointer type for `json.Marshaler` and `encoding.TextMarshaler`; if either implements a method, encode elements individually as `encoding/json` does.
- Invoke each delegated marshaler exactly once per preview. Avoid separate validation and preview traversals that call user methods twice; consume the one returned result while continuing validation of the remaining payload.
- Preserve valid oversized `json.Marshaler` output as a canonical truncated preview rather than treating size alone as unavailable. Validate the complete returned JSON once to retain invalid-output semantics, but compact and retain only the bounded visible prefix. Expect validation latency to scale with returned bytes even when preview allocations remain flat.
- Escape `encoding.TextMarshaler` output directly from returned bytes. Converting a large `[]byte` result to `string` creates a full-size caller-side copy even if the visible preview is short.
- Preserve established empty and failure behavior, such as an empty-value dash and the existing unavailable fallback for actual marshal errors, invalid custom JSON, unsupported values, and non-finite numbers.
- Keep raw `--json` or equivalent output on the complete serialization path; bounded work belongs only in the plain preview path.
- Verify surrounding event ordering and that rendering does not mutate the input sequence.

## Structs And Recursion

- Handle large strings nested in struct map, slice, and array fields before delegating the struct to `json.Marshal`; otherwise cloning or final serialization can still allocate in proportion to hidden payload size.
- Use one shared retained-data budget across a bounded struct clone. Cap container breadth, recursively clip retained scalars, and validate omitted elements only when their types can produce marshal errors. Primitive tails such as strings and booleans can be skipped after the visible/budgeted portion; interfaces, floats, unsupported kinds, and method-bearing values still require validation.
- Carry bounded cloning through the same supported nesting limit used by preview traversal, while preserving pointer/addressability semantics needed for pointer-receiver methods on nested fields.
- Never return the original subtree when the clone or traversal depth ceiling is reached. A deep pointer/interface chain can otherwise hand an oversized tail back to `json.Marshal`; return a deterministic depth error and use the established unavailable fallback instead.
- Preserve ordinary struct semantics through same-type bounded clones and `encoding/json` whenever possible. Manual field encoding risks diverging on embedded-field selection, conflicting tags, `omitempty`, and `,string`; reserve it for narrowly proven cases such as avoiding full nested marshaler output, with focused compatibility tests.
- Encode only the needed base64 source prefix for oversized ordinary `[]byte` values.
- Keep a deterministic nesting limit and retain late-value validation after the visible prefix is full so unsupported or invalid values do not silently become successful previews.

## Map Keys

- Support the same key classes as `encoding/json`: strings, signed and unsigned integers, and keys implementing `encoding.TextMarshaler`.
- Apply `TextMarshaler` dispatch before integer formatting. A named integer key that implements `MarshalText` must use the text result, matching `encoding/json` precedence.
- Do not reject a valid map solely because a key is oversized, and do not delegate the entire map to unbounded `json.Marshal`. Normalize supported keys, retain only the bounded bytes needed for display and comparison, and sort by canonical key text.
- Bound long-common-prefix comparisons. Once keys are indistinguishable beyond the retained prefix, their later ordering cannot affect the already-complete visible preview; avoid scanning megabytes merely to establish an invisible suffix order.
- Keep map values associated with their original reflected keys after sorting normalized key records, and continue validating values after output truncation.

## Bounding Work

- Bound retained output, recursion, object breadth where policy allows, map-key processing, and oversized scalar handling. Even after the visible prefix is complete, continue enough traversal to preserve existing later-value validity and error semantics.
- Audit every helper loop for hidden payload-proportional work. In particular, cap ASCII-run scans at the maximum bytes or cells needed to decide the preview.
- A caller cannot bound work or allocation performed inside an arbitrary custom marshaler. Separate method cost from previewer cost in benchmarks and document unavoidable complete validation scans where compatibility requires them.
- Treat bounded allocations as insufficient proof. Also measure bytes inspected and recursive work relative to displayed content, explicitly identifying exceptions such as complete custom-JSON validation and late container-value validation.
- Fall back deterministically when a pathological value cannot be represented safely within the implementation's bounds.

## Regression And Measurement Matrix

- Cover canonical small nested values, stable keys, escaping, combining and wide Unicode, malformed UTF-8, oversized strings, deep nesting, empty payloads, unavailable values, event ordering, and a large-payload raw JSON round trip.
- Add focused cases for pointer-receiver JSON and text marshalers on addressable slice elements and nested slice-element struct fields, failing methods, exactly-once invocation, invalid values after a truncated prefix, valid and invalid oversized custom output, deeply nested struct strings beyond former clone cutoffs, pathological pointer/interface depth, ordinary `[]byte`, named byte elements with value- or pointer-receiver marshalers, oversized TextMarshaler output, and long-common-prefix map keys.
- Test struct fields containing large maps, scalar slices, arrays, and wide primitive slices. Include nested custom and text marshalers so tests detect both payload-sized cloning and full struct-level JSON allocation.
- Test string, integer, TextMarshaler, and named-integer-TextMarshaler map keys against `encoding/json` output for ordinary cases, plus oversized key prefixes for bounded behavior.
- Benchmark one-event and representative batch rendering because table-rendering allocations can dominate and scale with event count independently of preview work.
- Run one-event and batch cases at 1 KiB, 64 KiB, and 1 MiB for ASCII and zero-width strings, shallow and deep structs, struct maps/slices/arrays, wide primitive slices, byte slices, JSON/Text marshaler output including nested struct fields, integer/TextMarshaler keys, and long-common-prefix string keys.
- Expect flat preview allocations across payload sizes. Most fixtures should also have flat latency; valid oversized custom JSON may scale in latency because preserving invalid-output semantics requires a complete validation scan.
- Interpret custom-marshaler benchmarks carefully: prebuilt returned bytes isolate preview validation and copying, while a marshaler that constructs output per call includes unavoidable user-method cost.
- If results unexpectedly scale, inspect scan limits, string/byte conversions, complete container clones, clone-depth returns, delegated struct `json.Marshal` calls, omitted-tail validation, and key comparisons before accepting the implementation. Correct accidental linear work and rerun the full matrix.
- Finish with `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`.
