package exif

// bigtiff_encode_guard_test.go — gate tests for audit finding #107.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: magic 0x002B, 64-bit offsets.
//   - TIFF 6.0 §2: magic 0x002A, 32-bit offsets.
//
// Test IDs:
//   TestEncodeBigTIFFSourceReturnsError — gate for exif.Encode guard (#107).
//   TestBigTIFFProvenance_FlagSet       — verify Parse sets BigTIFF=true for 0x002B.
//   TestBigTIFFProvenance_ClassicFalse  — verify Parse sets BigTIFF=false for 0x002A.

import (
	"encoding/binary"
	"errors"
	"testing"
)

// TestEncodeBigTIFFSourceReturnsError is the exif-level gate for audit finding #107.
//
// Before the fix: exif.Encode silently emitted a classic TIFF header (0x002A)
// for a BigTIFF-parsed EXIF, truncating all 64-bit offsets to 32 bits.
//
// After the fix: exif.Encode returns ErrBigTIFFEncodeNotSupported without
// emitting any bytes when e.BigTIFF is true.
//
// Two sub-cases cover the full surface:
//
//	(a) BigTIFF with a TypeLong8 entry — ensures the guard fires even when OOL
//	    entries use BigTIFF-only type codes.
//	(b) BigTIFF with only small offsets / standard types — ensures the guard
//	    fires regardless of offset magnitude; the corruption risk is the same
//	    because the encoder would still emit a 0x002A header.
func TestEncodeBigTIFFSourceReturnsError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		data func() []byte
	}{
		{
			// Case (a): BigTIFF with TypeLong8 entry.
			// BigTIFF spec §3.3: type code 16 = LONG8 (uint64, 8 bytes).
			name: "with_TypeLong8_entry",
			data: func() []byte {
				// Build a minimal BigTIFF: header + IFD with one TypeLong8 entry.
				const (
					hdrSize     = 16
					countSize   = 8
					entrySize   = 20
					nextPtrSize = 8
					nEntries    = 1
				)
				order := binary.LittleEndian
				ifdBlockSize := countSize + nEntries*entrySize + nextPtrSize
				ifdOff := uint64(hdrSize)
				dataOff := ifdOff + uint64(ifdBlockSize)
				buf := make([]byte, int(dataOff)+8)

				buf[0], buf[1] = 'I', 'I'
				order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
				order.PutUint16(buf[4:], 8)
				order.PutUint16(buf[6:], 0)
				order.PutUint64(buf[8:], ifdOff)

				ifdPos := int(ifdOff)
				order.PutUint64(buf[ifdPos:], nEntries)
				ifdPos += 8
				// StripOffsets (0x0111), TypeLong8 (16), count=1, OOL.
				order.PutUint16(buf[ifdPos:], 0x0111)
				order.PutUint16(buf[ifdPos+2:], 16) // TypeLong8
				order.PutUint64(buf[ifdPos+4:], 1)
				order.PutUint64(buf[ifdPos+12:], dataOff)
				order.PutUint64(buf[int(dataOff):], 0x1000000)
				return buf
			},
		},
		{
			// Case (b): BigTIFF with only small, standard-type entries.
			// Even though all values fit in 32 bits, the source is BigTIFF (magic
			// 0x002B) and must not be silently downgraded to classic TIFF (0x002A).
			name: "small_offsets_only",
			data: func() []byte {
				return buildBigTIFF(binary.LittleEndian, []bigTIFFEntry{
					{tag: 0x010F, typ: uint16(TypeASCII), count: 6, payload: []byte("Canon\x00")},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := tc.data()

			// Parse must succeed — BigTIFF read is fully supported.
			e, parseErr := Parse(data)
			if parseErr != nil {
				t.Fatalf("Parse: unexpected error: %v", parseErr)
			}
			if e == nil {
				t.Fatal("Parse: returned nil EXIF")
			}
			// Provenance flag must be set.
			if !e.BigTIFF {
				t.Error("Parse: EXIF.BigTIFF = false, want true for 0x002B source")
			}

			// Encode must return ErrBigTIFFEncodeNotSupported and zero bytes.
			// BigTIFF spec §2; audit finding #107: encoding would emit 0x002A
			// (classic TIFF) and truncate all 64-bit offsets to 32 bits.
			out, encErr := Encode(e)
			if encErr == nil {
				t.Errorf("Encode: expected ErrBigTIFFEncodeNotSupported, got nil (emitted %d bytes)", len(out))
				return
			}
			if !errors.Is(encErr, ErrBigTIFFEncodeNotSupported) {
				t.Errorf("Encode: error does not wrap ErrBigTIFFEncodeNotSupported: %v", encErr)
			}
			if len(out) != 0 {
				t.Errorf("Encode: returned %d bytes on error, want 0", len(out))
			}
		})
	}
}

// TestBigTIFFProvenance_FlagSet verifies that Parse sets EXIF.BigTIFF = true
// when the source magic is 0x002B (BigTIFF).
//
// BigTIFF spec §2; audit finding #107.
func TestBigTIFFProvenance_FlagSet(t *testing.T) {
	t.Parallel()

	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := "LE"
		if order == binary.BigEndian {
			name = "BE"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := buildBigTIFF(order, nil) // minimal BigTIFF, no entries
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse BigTIFF %s: %v", name, err)
			}
			if !e.BigTIFF {
				t.Errorf("BigTIFF %s: EXIF.BigTIFF = false, want true", name)
			}
		})
	}
}

// TestBigTIFFProvenance_ClassicFalse verifies that Parse sets EXIF.BigTIFF = false
// for a classic TIFF source (magic 0x002A). This is the regression guard: the
// BigTIFF provenance flag must not be set for classic TIFF inputs.
//
// TIFF 6.0 §2; audit finding #107 (regression guard).
func TestBigTIFFProvenance_ClassicFalse(t *testing.T) {
	t.Parallel()
	// Build a minimal classic TIFF LE.
	makeBytes := asciiPayload("TestMake")
	order := binary.LittleEndian
	const hdrSize = 8
	const entrySize = 12
	ifdOff := uint32(hdrSize)
	valueAreaOff := ifdOff + 2 + entrySize + 4
	buf := make([]byte, int(valueAreaOff)+len(makeBytes))

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A) // classic TIFF magic
	order.PutUint32(buf[4:], ifdOff)

	order.PutUint16(buf[ifdOff:], 1) // 1 entry
	p := int(ifdOff) + 2
	order.PutUint16(buf[p:], 0x010F)                   // Make tag
	order.PutUint16(buf[p+2:], 2)                      // TypeASCII
	order.PutUint32(buf[p+4:], uint32(len(makeBytes))) //nolint:gosec // G115: test data, bounded
	order.PutUint32(buf[p+8:], valueAreaOff)
	copy(buf[valueAreaOff:], makeBytes)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse classic TIFF: %v", err)
	}
	if e.BigTIFF {
		t.Error("Classic TIFF: EXIF.BigTIFF = true, want false")
	}

	// Encode must succeed for a classic TIFF source (positive control).
	out, encErr := Encode(e)
	if encErr != nil {
		t.Errorf("Encode classic TIFF: unexpected error: %v", encErr)
	}
	if len(out) == 0 {
		t.Error("Encode classic TIFF: returned 0 bytes, want non-empty")
	}
	// Output must carry classic TIFF magic.
	if len(out) >= 4 && order.Uint16(out[2:]) != 0x002A {
		t.Errorf("Encode classic TIFF: output magic = 0x%04X, want 0x002A", order.Uint16(out[2:]))
	}
}
