package iptc

// iptc_audit146_179_test.go — regression gates for audit findings #146 and #179.
//
//   #146 — IPTC Encode must emit datasets in ascending DataSet-number order
//           within each record (IIM §2.2 SHOULD).
//   #179 — IPTC Encode must not emit duplicate 1:00 EnvelopeRecordVersion
//           markers (IIM §1.5(v) MUST-NOT-REPEAT).

import (
	"bytes"
	"testing"
)

// TestIPTCEncodeAscendingDatasetOrder is the gate for audit finding #146.
//
// IIM §2.2 SHOULD: datasets within a record shall be ordered by dataset
// number in ascending order. This test populates Record 2 with datasets in
// reverse / out-of-order insertion sequence, then encodes and verifies that
// the emitted stream honours ascending DataSet-number order.
//
// Repeatable datasets (e.g. 2:25 Keywords) with the same number must preserve
// their relative insertion order (stable sort).
func TestIPTCEncodeAscendingDatasetOrder(t *testing.T) {
	t.Parallel()

	t.Run("AuditFinding146/byline-before-keyword", func(t *testing.T) {
		t.Parallel()
		// Add Byline (2:80) before Keywords (2:25) — insertion order is
		// descending by dataset number.
		i := new(IPTC)
		i.SetCreator("Alice") // 2:80
		i.AddKeyword("bird")  // 2:25

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		// Collect Record-2 dataset numbers in emission order, skipping 2:00.
		var order []uint8
		for pos := 0; pos+5 <= len(enc); pos++ {
			if enc[pos] == 0x1C && enc[pos+1] == 0x02 && enc[pos+2] != 0x00 {
				order = append(order, enc[pos+2])
			}
		}
		// Expect ascending: 2:25 (25) before 2:80 (80).
		for idx := 1; idx < len(order); idx++ {
			if order[idx] < order[idx-1] {
				t.Errorf("AuditFinding146: dataset order not ascending at position %d: %v",
					idx, order)
				break
			}
		}
		// Specifically: 2:25 must precede 2:80.
		pos25, pos80 := -1, -1
		for idx, ds := range order {
			if ds == 25 && pos25 < 0 {
				pos25 = idx
			}
			if ds == 80 && pos80 < 0 {
				pos80 = idx
			}
		}
		if pos25 < 0 {
			t.Fatal("AuditFinding146: 2:25 (Keywords) not found in encoded stream")
		}
		if pos80 < 0 {
			t.Fatal("AuditFinding146: 2:80 (Byline) not found in encoded stream")
		}
		if pos25 > pos80 {
			t.Errorf("AuditFinding146: 2:25 (Keywords) at index %d appears after 2:80 (Byline) at index %d; want ascending order",
				pos25, pos80)
		}
	})

	t.Run("AuditFinding146/keywords-25-before-byline-80", func(t *testing.T) {
		t.Parallel()
		// More datasets in explicit descending insertion order.
		i := new(IPTC)
		i.SetCaption("test caption") // 2:120
		i.SetCreator("Bob")          // 2:80
		i.AddKeyword("alpha")        // 2:25
		i.AddKeyword("beta")         // 2:25 (second occurrence — same number)

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		// Collect all Record-2 dataset numbers in emission order (excluding 2:00).
		var order []uint8
		for pos := 0; pos+5 <= len(enc); pos++ {
			if enc[pos] == 0x1C && enc[pos+1] == 0x02 && enc[pos+2] != 0x00 {
				order = append(order, enc[pos+2])
			}
		}

		// Full ascending-order assertion.
		for idx := 1; idx < len(order); idx++ {
			if order[idx] < order[idx-1] {
				t.Errorf("AuditFinding146: dataset order not ascending at index %d (ds=%d after ds=%d): full order %v",
					idx, order[idx], order[idx-1], order)
				return
			}
		}
	})

	t.Run("AuditFinding146/stable-order-for-repeatable-2:25", func(t *testing.T) {
		t.Parallel()
		// Repeatable datasets with identical numbers must preserve insertion order
		// (IIM §2.2.17: Keywords repeat — ordering among occurrences is significant
		// to callers who rely on position).
		i := new(IPTC)
		i.SetCreator("Carol")     // 2:80 — inserted first
		i.AddKeyword("first-kw")  // 2:25 — lower dataset number, inserted second
		i.AddKeyword("second-kw") // 2:25 — same number, inserted third
		i.AddKeyword("third-kw")  // 2:25 — same number, inserted fourth

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		// Parse the encoded stream and verify relative keyword order is stable.
		i2, err := Parse(enc)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		kws := i2.Keywords()
		if len(kws) != 3 {
			t.Fatalf("AuditFinding146/stable: Keywords count=%d, want 3", len(kws))
		}
		if kws[0] != "first-kw" || kws[1] != "second-kw" || kws[2] != "third-kw" {
			t.Errorf("AuditFinding146/stable: keyword order = %v, want [first-kw second-kw third-kw]", kws)
		}
	})

	t.Run("AuditFinding146/receiver-not-mutated", func(t *testing.T) {
		t.Parallel()
		// FINDING-002 constraint: Encode must not mutate the receiver.
		// Calling Encode twice must produce identical output regardless of
		// in-place mutations that would happen if Encode sorted i.Records[] directly.
		i := new(IPTC)
		i.SetCreator("Dave") // 2:80 inserted first
		i.AddKeyword("kw1")  // 2:25 inserted second

		enc1, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode (1): %v", err)
		}
		enc2, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode (2): %v", err)
		}
		if !bytes.Equal(enc1, enc2) {
			t.Error("AuditFinding146/receiver-not-mutated: two Encode calls on same *IPTC produced different output")
		}

		// Also verify insertion order in i.Records[2] is unchanged after Encode.
		// i.Records[2] must still have Byline at index 0, Keywords at index 1.
		if len(i.Records[2]) < 2 {
			t.Fatalf("AuditFinding146/receiver-not-mutated: Records[2] len=%d, want >=2", len(i.Records[2]))
		}
		if i.Records[2][0].DataSet != DS2Byline {
			t.Errorf("AuditFinding146/receiver-not-mutated: Records[2][0].DataSet=%d, want %d (DS2Byline); receiver was mutated",
				i.Records[2][0].DataSet, DS2Byline)
		}
		if i.Records[2][1].DataSet != DS2Keywords {
			t.Errorf("AuditFinding146/receiver-not-mutated: Records[2][1].DataSet=%d, want %d (DS2Keywords); receiver was mutated",
				i.Records[2][1].DataSet, DS2Keywords)
		}
	})
}

// TestEncodeNoDuplicateRecordVersion is the gate for audit finding #179.
//
// IIM §1.5(v) MUST-NOT-REPEAT: the 1:00 EnvelopeRecordVersion dataset must
// appear at most once in a well-formed IIM stream. Before this fix, two paths
// in Encode both emitted 1:00: the UTF-8 preamble block (when emitUTF8Decl is
// true) and the main-loop Record-1 version injection, so streams with both
// (a) a non-ASCII value — which triggers the UTF-8 preamble — and
// (b) at least one caller-stored Record-1 non-version dataset
// would contain two 1:00 markers.
func TestEncodeNoDuplicateRecordVersion(t *testing.T) {
	t.Parallel()

	t.Run("AuditFinding179/r1-nonversion-plus-non-ascii-exactly-one-1:00", func(t *testing.T) {
		t.Parallel()
		// Reproduce the exact condition described in the finding:
		//   Records[1] holds a non-version dataset (1:20) AND
		//   a non-ASCII caption triggers the UTF-8 declaration preamble.
		// Before the fix: two 1:00 markers. After the fix: exactly one.
		i := new(IPTC)
		i.Records[1] = append(i.Records[1], Dataset{Record: 1, DataSet: 20, Value: []byte("x")})
		i.SetCaption("café") // non-ASCII → emitUTF8Decl = true

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		// Count occurrences of 1:00 (1C 01 00).
		count := 0
		for pos := 0; pos+3 <= len(enc); pos++ {
			if enc[pos] == 0x1C && enc[pos+1] == 0x01 && enc[pos+2] == 0x00 {
				count++
			}
		}
		if count != 1 {
			t.Errorf("AuditFinding179: encoded stream contains %d 1:00 (EnvelopeRecordVersion) markers, want exactly 1",
				count)
		}
	})

	t.Run("AuditFinding179/r1-only-ascii-exactly-one-1:00", func(t *testing.T) {
		t.Parallel()
		// ASCII-only content: UTF-8 preamble is NOT emitted.
		// 1:00 must still appear exactly once (from the main-loop injection).
		i := new(IPTC)
		i.Records[1] = append(i.Records[1], Dataset{Record: 1, DataSet: 20, Value: []byte("ascii")})
		i.SetCaption("pure ascii")

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		count := 0
		for pos := 0; pos+3 <= len(enc); pos++ {
			if enc[pos] == 0x1C && enc[pos+1] == 0x01 && enc[pos+2] == 0x00 {
				count++
			}
		}
		if count != 1 {
			t.Errorf("AuditFinding179/ascii-only: encoded stream has %d 1:00 markers, want 1", count)
		}
	})

	t.Run("AuditFinding179/utf8-decl-only-no-r1-datasets-exactly-one-1:00", func(t *testing.T) {
		t.Parallel()
		// Non-ASCII caption, no caller-stored Record-1 datasets.
		// The preamble emits 1:00; the main loop has no Record-1 datasets to process.
		i := new(IPTC)
		i.SetCaption("naïve")

		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		count := 0
		for pos := 0; pos+3 <= len(enc); pos++ {
			if enc[pos] == 0x1C && enc[pos+1] == 0x01 && enc[pos+2] == 0x00 {
				count++
			}
		}
		if count != 1 {
			t.Errorf("AuditFinding179/utf8-only: encoded stream has %d 1:00 markers, want 1", count)
		}
	})

	t.Run("AuditFinding179/round-trip-stable", func(t *testing.T) {
		t.Parallel()
		// Encode → Parse → Encode must be idempotent and produce exactly one 1:00.
		i := new(IPTC)
		i.Records[1] = append(i.Records[1], Dataset{Record: 1, DataSet: 20, Value: []byte("x")})
		i.SetCaption("café")

		enc1, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode (1): %v", err)
		}
		i2, err := Parse(enc1)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		enc2, err := Encode(i2)
		if err != nil {
			t.Fatalf("Encode (2): %v", err)
		}

		count := 0
		for pos := 0; pos+3 <= len(enc2); pos++ {
			if enc2[pos] == 0x1C && enc2[pos+1] == 0x01 && enc2[pos+2] == 0x00 {
				count++
			}
		}
		if count != 1 {
			t.Errorf("AuditFinding179/round-trip: re-encoded stream has %d 1:00 markers, want 1", count)
		}
	})
}
