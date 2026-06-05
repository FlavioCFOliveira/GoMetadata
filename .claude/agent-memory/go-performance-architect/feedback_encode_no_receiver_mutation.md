---
name: encode-no-receiver-mutation
description: Encode functions must never mutate their receiver — FINDING-002 pattern in iptc.Encode
metadata:
  type: feedback
---

Encode (and similar serialisation functions) must NOT mutate the input struct. In FINDING-002, `iptc.Encode` appended to `i.Records[0]` to update the internal UTF-8 flag when auto-injecting the 1:90 declaration. This caused a data race when two goroutines called Encode concurrently on the same `*IPTC`.

**Why:** The caller has no way to know Encode has a side effect. Mutation of a shared struct without a lock is a data race under `-race`. Encode is pure serialisation: its output is bytes; the input must remain unchanged.

**How to apply:** When serialising, compute derived state (e.g. "needs UTF-8 declaration") from the input only for output purposes. Never write back to the input struct. If post-encode state update is needed, document it as a separate method (e.g. `MarkUTF8()`), never hide it inside Encode.

Verified: the fix removes the `i.Records[0] = append(...)` mutation; all goroutine outputs are byte-identical under `-race`; `TestConcurrentEncodeNonASCII` passes.
