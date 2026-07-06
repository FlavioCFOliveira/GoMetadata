---
name: feedback_metadata_mutex_guard
description: Metadata struct has sync.Mutex mu; all Set* methods must hold it for their entire body including ensure* calls
metadata:
  type: feedback
---

The Metadata struct contains `mu sync.Mutex`. Every Set* method must acquire `m.mu.Lock(); defer m.mu.Unlock()` before calling any ensure* helper or sub-struct mutation.

**Why:** ensure* helpers do check-then-act on pointer fields (nil check → assign). Without the mutex, concurrent Set* goroutines race on these fields and on the underlying xmp map writes. Audit finding #128. Prior to the fix, a race detector run with 10+ goroutines calling SetCaption produced DATA RACE warnings.

**How to apply:** When adding a new Set* method, always include the mutex guard as the first two lines. The ensure* helpers (ensureEXIF/ensureIPTC/ensureXMP) are called while the lock is held and must never acquire mu themselves (they are not recursive). Metadata must not be copied by value after first use — it contains a sync.Mutex.
