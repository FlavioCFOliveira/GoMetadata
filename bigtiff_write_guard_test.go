package gometadata

// bigtiff_write_guard_test.go — gate tests for audit finding #107 / task #271.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: magic 0x002B, 16-byte header,
//     8-byte IFD offsets, 64-bit counts; offset bytesize = 8.
//   - TIFF 6.0 §2: magic 0x002A, 8-byte header, 32-bit IFD offsets.
//
// History:
//   - Audit finding #107 (pre-task #264/#270/#271): gometadata.Write on a
//     BigTIFF source silently emitted a classic TIFF (0x002A) header,
//     truncating every 64-bit offset to 32 bits — a silent corruption risk.
//     The immediate fix was a fast-fail guard: Write returned
//     ErrWriteNotSupported for BigTIFF sources before any I/O.
//   - Task #264: exif.Encode gained a native BigTIFF write path
//     (serialiseBigTIFF); the underlying EXIF blob encoder was ready, but the
//     root package's guard (isBigTIFFSource in write.go) still refused all
//     BigTIFF writes at the top-level API.
//   - Task #270: format/tiff's copy-and-relocate serializer became
//     container-width-aware for the surrounding image blocks, SubIFDs, and
//     raw offset scans, closing the second half of the BigTIFF write gap.
//   - Task #271: with both layers verified end-to-end, the root-package gate
//     (isBigTIFFSource + the ErrWriteNotSupported short-circuit in
//     writeTIFF) was removed. gometadata.Write now writes BigTIFF sources
//     successfully. TestBigTIFFWriteReturnsError (which pinned the pre-#271
//     refusal behaviour) is replaced by TestBigTIFFWriteSucceeds below.
//
// Gate tests:
//   TestBigTIFFWriteSucceeds         — gometadata.Write on BigTIFF: succeeds,
//                                       output re-parses and preserves the
//                                       BigTIFF magic (0x002B).
//   TestBigTIFFWriteClassicPositive  — gometadata.Write on classic TIFF still
//                                       succeeds (regression guard, unchanged
//                                       by task #271).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildBigTIFFForWriteGuardTest constructs a minimal BigTIFF byte stream for
// use in write-guard tests.  Two variants are provided:
//
//   - withLong8=true:  includes a TypeLong8 (type 16) entry — the canonical
//     BigTIFF-specific type used for large StripOffsets.
//   - withLong8=false: uses only standard TIFF types with small values;
//     proves the write path succeeds on magic alone, not on type codes.
//
// BigTIFF spec §2: magic 0x002B, 16-byte header, 8-byte offsets.
func buildBigTIFFForWriteGuardTest(withLong8 bool) []byte {
	order := binary.LittleEndian
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8
		nEntries    = 1
	)
	ifdBlockSize := countSize + nEntries*entrySize + nextPtrSize
	ifdOff := uint64(hdrSize)
	dataOff := ifdOff + uint64(ifdBlockSize)

	// dataSz: bytes of OOL value data.
	// For withLong8: one uint64 strip offset (8 bytes).
	// For !withLong8: "CameraNN\x00" = 9 bytes > 8 (BigTIFF inline threshold),
	// so the value is out-of-line. This exercises standard-type OOL entries.
	var dataSz int
	if withLong8 {
		dataSz = 8
	} else {
		dataSz = 9
	}

	buf := make([]byte, int(dataOff)+dataSz)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B) // BigTIFF magic — audit finding #107
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], ifdOff)

	ifdPos := int(ifdOff)
	order.PutUint64(buf[ifdPos:], nEntries)
	ifdPos += 8

	if withLong8 {
		// StripOffsets (0x0111), TypeLong8 (16), count=1, OOL value.
		// BigTIFF spec §3.3: LONG8 = uint64, 8 bytes per element.
		order.PutUint16(buf[ifdPos:], 0x0111)
		order.PutUint16(buf[ifdPos+2:], 16) // TypeLong8
		order.PutUint64(buf[ifdPos+4:], 1)
		order.PutUint64(buf[ifdPos+12:], dataOff)
		order.PutUint64(buf[int(dataOff):], 0x0000_0001_0000_0000) // offset > 4 GiB
	} else {
		// Make (0x010F), TypeASCII, count=9, OOL (9 > 8 BigTIFF inline threshold).
		// This exercises the write path for a BigTIFF with only standard type codes.
		const payload = "CameraNN\x00"
		order.PutUint16(buf[ifdPos:], 0x010F)
		order.PutUint16(buf[ifdPos+2:], 2) // TypeASCII
		order.PutUint64(buf[ifdPos+4:], uint64(len(payload)))
		order.PutUint64(buf[ifdPos+12:], dataOff)
		copy(buf[int(dataOff):], payload)
	}
	return buf
}

// TestBigTIFFWriteSucceeds is the top-level gate for task #271.
//
// Before task #271: gometadata.Write on a BigTIFF source returned
// ErrWriteNotSupported and wrote zero bytes, even though both underlying
// layers (exif.Encode since task #264, format/tiff's relocator since #270)
// were already capable of producing a correct BigTIFF output.
//
// After task #271: gometadata.Write succeeds for BigTIFF sources. The output
// re-parses without error and preserves the BigTIFF magic (0x002B) — it must
// never be silently downgraded to classic TIFF (0x002A), which would
// truncate every 64-bit offset.
//
// Two sub-cases cover both variants described in the original guard test:
//
//	(a) BigTIFF with a TypeLong8 entry — canonical large-file BigTIFF.
//	(b) BigTIFF with only small/standard-type entries — proves the write path
//	    succeeds regardless of actual offset magnitude, not just when a
//	    64-bit-only type code happens to be present.
//
// BigTIFF spec §2; audit finding #107; tasks #264/#270/#271.
func TestBigTIFFWriteSucceeds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		withLong8 bool
	}{
		{"with_TypeLong8_entry", true},
		{"small_offsets_only", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildBigTIFFForWriteGuardTest(tc.withLong8)

			// Read must succeed — BigTIFF read is fully supported.
			m, readErr := Read(bytes.NewReader(data))
			if readErr != nil {
				t.Fatalf("Read BigTIFF: unexpected error: %v", readErr)
			}

			// Write must now succeed (task #271) and produce non-empty output.
			var out bytes.Buffer
			writeErr := Write(bytes.NewReader(data), &out, m)
			if writeErr != nil {
				t.Fatalf("Write BigTIFF: unexpected error: %v", writeErr)
			}
			if out.Len() == 0 {
				t.Fatal("Write BigTIFF: produced no output bytes")
			}

			// Output must re-parse without error and still carry BigTIFF magic
			// 0x002B — a silent downgrade to classic TIFF would truncate every
			// 64-bit offset (the original audit finding #107 corruption).
			if out.Len() < 4 || binary.LittleEndian.Uint16(out.Bytes()[2:]) != 0x002B {
				t.Fatalf("Write BigTIFF: output magic = 0x%04X, want 0x002B (BigTIFF must not be downgraded)",
					binary.LittleEndian.Uint16(out.Bytes()[2:]))
			}

			m2, reReadErr := Read(bytes.NewReader(out.Bytes()))
			if reReadErr != nil {
				t.Fatalf("Read (round-trip) BigTIFF output: unexpected error: %v", reReadErr)
			}
			if m2.EXIF == nil || !m2.EXIF.BigTIFF {
				t.Error("Read (round-trip) BigTIFF output: EXIF.BigTIFF = false, want true")
			}
		})
	}
}

// TestBigTIFFWriteClassicPositive verifies that BigTIFF write support does
// NOT change behaviour for classic TIFF sources (magic 0x002A).
//
// This is the regression guard: enabling BigTIFF writes must not accidentally
// alter or break valid classic TIFF writes. Unchanged by task #271.
//
// TIFF 6.0 §2; audit finding #107 (regression guard).
func TestBigTIFFWriteClassicPositive(t *testing.T) {
	t.Parallel()

	data := minimalTIFFPayload() // classic TIFF, magic 0x002A

	// Verify it is indeed classic TIFF.
	if len(data) < 4 || binary.LittleEndian.Uint16(data[2:]) != 0x002A {
		t.Fatalf("test invariant: minimalTIFFPayload magic = 0x%04X, want 0x002A",
			binary.LittleEndian.Uint16(data[2:]))
	}

	m, readErr := Read(bytes.NewReader(data))
	if readErr != nil {
		t.Fatalf("Read classic TIFF: %v", readErr)
	}

	var out bytes.Buffer
	writeErr := Write(bytes.NewReader(data), &out, m)
	if writeErr != nil {
		t.Errorf("Write classic TIFF: unexpected error (BigTIFF support must not affect classic TIFF): %v", writeErr)
	}
	if out.Len() == 0 {
		t.Error("Write classic TIFF: produced no output bytes")
	}

	// Output must carry classic TIFF magic (not BigTIFF).
	if out.Len() >= 4 && binary.LittleEndian.Uint16(out.Bytes()[2:]) != 0x002A {
		t.Errorf("Write classic TIFF: output magic = 0x%04X, want 0x002A",
			binary.LittleEndian.Uint16(out.Bytes()[2:]))
	}
}
