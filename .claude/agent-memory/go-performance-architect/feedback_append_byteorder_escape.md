---
name: append-byteorder-escape
description: binary.AppendByteOrder type assertion eliminates [N]byte→append heap escapes; use comma-ok (task #247), not a direct assertion (task #201 was reverted for panic safety)
metadata:
  type: feedback
---

When a fixed-size stack array (`var x [N]byte`) is passed to `append` via `x[:]`, the
compiler moves the array to the heap. The correct fix is `binary.AppendByteOrder`:

```go
// BEFORE (escapes to heap):
var countB [2]byte
order.PutUint16(countB[:], uint16(n))
out = append(out, countB[:]...)

// AFTER (no escape, panic-free — task #247 final form):
func appendUint16Order(out []byte, order binary.ByteOrder, v uint16) []byte {
	if ao, ok := order.(binary.AppendByteOrder); ok {
		return ao.AppendUint16(out, v)
	}
	var b [2]byte
	order.PutUint16(b[:], v)
	return append(out, b[:]...)
}
```

**Why:** Both `binary.LittleEndian` and `binary.BigEndian` implement `AppendByteOrder`
(Go 1.21+), so the fast path above is taken for every order this package's own
parse/encode paths ever produce — zero-alloc, byte-identical to a direct assertion.
Task #201's original implementation used a **direct** (non-comma-ok) assertion
`appOrd := order.(binary.AppendByteOrder)` cached once per function and reused across
multiple `Append*` calls. That panicked (audit finding PERF-201-LOW, task #247) for any
caller-supplied `binary.ByteOrder` that does not implement `AppendByteOrder` — reachable
via `&EXIF{ByteOrder: <custom impl>}` + `Encode`, not attacker-reachable but a real public-API
panic surface. The fix wraps the assertion in a comma-ok helper
(`appendUint16Order`/`appendUint32Order` in `exif/write.go`) with a `PutUint16`-into-scratch
fallback that works for any `binary.ByteOrder`. Confirmed via benchstat: 0% B/op and allocs/op
delta on `BenchmarkEXIFEncode`/`BenchmarkEXIFEncode_Camera` (fast path fully preserved); ~+3ns
(~2-3%) ns/op noise-level cost because the wrapper function itself does not fit the compiler's
80-cost inline budget (measured cost 148-154) — tried factoring the fallback into its own
function to shrink the wrapper below budget; did not help (cost only got worse), so the
extra call-frame is accepted as the cost of panic safety.

**nolint rule (still applies to any remaining direct assertion elsewhere):** The `revive`
linter fires on `unchecked-type-assertion` even when `//nolint:forcetypeassert` is present —
both linter names must be listed: `//nolint:forcetypeassert,revive`. The comma-ok form needs
no nolint at all.

**How to apply:** Any site where a `[N]byte` stack var is populated via `order.PutUint*`
then passed to `append` is a candidate for the append-byteorder pattern. If `order` is always
one of the two package-level singletons, a direct assertion looks tempting — resist it; use
the comma-ok wrapper pattern above so a hand-built `EXIF{ByteOrder: ...}` can never panic
`Encode`. Verify escapes with `go build -gcflags='-m=2' ./pkg/... 2>&1 | grep 'moved to heap'`.
See `exif/write.go` (`appendUint16Order`/`appendUint32Order`, used by both `writeTIFFHeader`
in write.go and `writeIFD` in ifd.go).
