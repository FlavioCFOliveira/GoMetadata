package gometadata

// bigtiff_write_e2e_test.go — public-API end-to-end gate for task #271.
//
// TestWriteBigTIFFEndToEnd proves that gometadata.Write, the top-level public
// entry point, writes BigTIFF containers successfully now that the
// root-package refusal gate (isBigTIFFSource in write.go) has been removed.
//
// It uses the two REAL, committed BigTIFF fixtures
// (testdata/fixtures/BigTIFF_{LE,BE}.tif — produced by `tiffcp -8`, already
// exercised on the read side by TestReadBigTIFF in read_test.go) and drives
// the full public round trip: Read -> SetCopyright -> Write -> Read again.
//
// Assertions:
//   - Write returns no error and produces non-empty output.
//   - The output re-parses and still carries the BigTIFF magic (0x002B) in
//     the fixture's own byte order — no silent downgrade to classic TIFF
//     (0x002A), which would truncate every 64-bit offset (audit finding
//     #107's original corruption).
//   - Pre-existing EXIF metadata not touched by the mutation (Make, Model,
//     ImageWidth, ImageLength) survives byte-for-byte.
//   - The newly-set field (Copyright) is present in the round-tripped output.
//   - The image data itself — the StripOffsets/StripByteCounts-addressed
//     pixel payload — is preserved byte-for-byte, even though its absolute
//     file offset necessarily changes across the copy-and-relocate write
//     (BigTIFF spec §3.3: StripOffsets/StripByteCounts are TypeLong8 in both
//     fixtures).
//
// This is the top-level counterpart to the exif-package gate
// (TestEncodeBigTIFFSourceSucceeds, task #264) and the format/tiff-package
// gate (TestConformance_R18_bigtiff_roundtrip_fidelity_*, task #270): those
// prove the two underlying layers are correct in isolation, while this test
// proves gometadata.Write — the only entry point most callers ever use —
// actually reaches them.

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// bigTIFFStrip extracts the single-strip image payload addressed by IFD0's
// StripOffsets (0x0111) / StripByteCounts (0x0117) entries from raw, decoded
// using e's byte order.
//
// Both committed fixtures declare StripOffsets/StripByteCounts as TypeLong8
// (BigTIFF spec §3.3: 8 bytes/element) with Count=1 — exactly the BigTIFF
// inline threshold (8 bytes) — so IFDEntry.Value already holds the decoded
// 8-byte field verbatim rather than pointing to a further out-of-line area.
// If that invariant does not hold (e.g. a future fixture change), this
// helper fails the test outright via t.Fatal rather than silently skipping
// the image-data assertion, per project policy (t.Skip is reserved for
// absent corpus files, OS/privilege limits, and stale boundary constants —
// none of which apply here).
func bigTIFFStrip(t *testing.T, raw []byte, e *exif.EXIF) []byte {
	t.Helper()
	if e == nil || e.IFD0 == nil {
		t.Fatal("bigTIFFStrip: EXIF or IFD0 is nil")
	}
	off := e.IFD0.Get(exif.TagStripOffsets)
	cnt := e.IFD0.Get(exif.TagStripByteCounts)
	if off == nil || cnt == nil {
		t.Fatal("bigTIFFStrip: StripOffsets/StripByteCounts entry missing from IFD0")
	}
	if len(off.Value) != 8 || len(cnt.Value) != 8 {
		t.Fatalf("bigTIFFStrip: unexpected StripOffsets/StripByteCounts encoding (offLen=%d cntLen=%d, want 8/8)",
			len(off.Value), len(cnt.Value))
	}
	offset := e.ByteOrder.Uint64(off.Value)
	length := e.ByteOrder.Uint64(cnt.Value)
	end := offset + length
	if end < offset || end > uint64(len(raw)) {
		t.Fatalf("bigTIFFStrip: strip range [%d:%d] exceeds buffer length %d", offset, end, len(raw))
	}
	return raw[offset:end]
}

// bigTIFFDimension decodes a single IFD0 entry that is known to hold
// ImageWidth (0x0100) or ImageLength (0x0101), accepting either TypeShort
// (as used by both committed BigTIFF fixtures) or TypeLong, mirroring the
// decode used by TestReadBigTIFF in read_test.go.
func bigTIFFDimension(t *testing.T, entry *exif.IFDEntry, tagName string) uint32 {
	t.Helper()
	if entry == nil {
		t.Fatalf("bigTIFFDimension: IFD0 %s entry missing", tagName)
	}
	switch entry.Type {
	case exif.TypeShort:
		return uint32(entry.Uint16())
	default:
		return entry.Uint32()
	}
}

func TestWriteBigTIFFEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		wantMake     = "Canon"
		wantModel    = "Canon EOS DIGITAL REBEL"
		wantWidth    = uint32(160)
		wantHeight   = uint32(120)
		newCopyright = "(c) 2026 task-271 e2e round-trip"
	)

	tests := []struct {
		name    string
		fixture string
	}{
		{"BigTIFF_LE", "testdata/fixtures/BigTIFF_LE.tif"},
		{"BigTIFF_BE", "testdata/fixtures/BigTIFF_BE.tif"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read fixture %q: %v", tc.fixture, err)
			}

			// Independent parse of the pristine bytes, used ONLY to snapshot the
			// "before" strip payload. Kept fully separate from m.EXIF below so
			// that the later mutation can never retroactively change this
			// snapshot.
			origEXIF, err := exif.Parse(data)
			if err != nil {
				t.Fatalf("exif.Parse (pristine snapshot): %v", err)
			}
			if !origEXIF.BigTIFF {
				t.Fatal("test invariant: fixture is not BigTIFF-provenanced")
			}
			beforeStrip := bigTIFFStrip(t, data, origEXIF)
			if len(beforeStrip) == 0 {
				t.Fatal("test invariant: fixture strip payload is empty")
			}

			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.EXIF == nil || !m.EXIF.BigTIFF {
				t.Fatal("Read: EXIF.BigTIFF = false, want true")
			}

			// Sanity-check pre-existing metadata before mutating.
			if got := m.Make(); got != wantMake {
				t.Fatalf("Make() = %q, want %q (before Write)", got, wantMake)
			}
			if got := m.CameraModel(); got != wantModel {
				t.Fatalf("CameraModel() = %q, want %q (before Write)", got, wantModel)
			}

			// Mutate: exercises the full write path (SetCopyright auto-creates
			// IPTC and XMP components in addition to editing EXIF), proving this
			// is a real metadata-editing round trip, not a bare pass-through copy.
			m.SetCopyright(newCopyright)

			var out bytes.Buffer
			if err := Write(bytes.NewReader(data), &out, m); err != nil {
				t.Fatalf("Write: unexpected error (task #271: BigTIFF write must be supported): %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("Write: produced no output bytes")
			}

			// The output must never be silently downgraded to classic TIFF
			// (0x002A) — that would truncate every 64-bit offset (audit finding
			// #107). Byte order is determined from the output's own marker, not
			// assumed, since Write must preserve whichever order the source used.
			outBytes := out.Bytes()
			if len(outBytes) < 4 {
				t.Fatalf("Write: output too short (%d bytes)", len(outBytes))
			}
			var order binary.ByteOrder = binary.LittleEndian
			if outBytes[0] == 'M' && outBytes[1] == 'M' {
				order = binary.BigEndian
			}
			if got := order.Uint16(outBytes[2:4]); got != 0x002B {
				t.Fatalf("Write: output magic = 0x%04X, want 0x002B (BigTIFF must not be downgraded)", got)
			}

			m2, err := Read(bytes.NewReader(outBytes))
			if err != nil {
				t.Fatalf("Read (round-trip): %v", err)
			}
			if m2.Format() != format.FormatTIFF {
				t.Errorf("Read (round-trip): Format() = %v, want FormatTIFF", m2.Format())
			}
			if m2.EXIF == nil || !m2.EXIF.BigTIFF {
				t.Fatal("Read (round-trip): EXIF.BigTIFF = false, want true")
			}

			// Unmodified metadata must survive byte-for-byte.
			if got := m2.Make(); got != wantMake {
				t.Errorf("Read (round-trip): Make() = %q, want %q", got, wantMake)
			}
			if got := m2.CameraModel(); got != wantModel {
				t.Errorf("Read (round-trip): CameraModel() = %q, want %q", got, wantModel)
			}

			widthEntry := m2.EXIF.IFD0.Get(exif.TagImageWidth)
			if got := bigTIFFDimension(t, widthEntry, "ImageWidth"); got != wantWidth {
				t.Errorf("Read (round-trip): ImageWidth = %d, want %d", got, wantWidth)
			}
			heightEntry := m2.EXIF.IFD0.Get(exif.TagImageLength)
			if got := bigTIFFDimension(t, heightEntry, "ImageLength"); got != wantHeight {
				t.Errorf("Read (round-trip): ImageLength = %d, want %d", got, wantHeight)
			}

			// The modified field must reflect the new value.
			if got := m2.Copyright(); got != newCopyright {
				t.Errorf("Read (round-trip): Copyright() = %q, want %q", got, newCopyright)
			}

			// Image data: the strip payload must be preserved byte-for-byte, even
			// though its absolute file offset necessarily changed across the
			// copy-and-relocate write.
			afterStrip := bigTIFFStrip(t, outBytes, m2.EXIF)
			if !bytes.Equal(beforeStrip, afterStrip) {
				t.Errorf("Read (round-trip): strip payload changed across Write "+
					"(before %d bytes, after %d bytes) — image data corrupted",
					len(beforeStrip), len(afterStrip))
			}
		})
	}
}
