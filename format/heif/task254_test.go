package heif

import (
	"bytes"
	"testing"
)

// Task #254: HEIF EXIF-extraction gap.
//
// Root cause: the data_reference_index omission fixed by task #282 (every
// affected corpus file stores its Exif item with construction_method 0).
// The tests below additionally pin the read semantics of every
// construction_method of ISO/IEC 14496-12 §8.11.3.3:
//
//	0 file offset — extents are absolute file positions (mdat)
//	1 idat offset — extents are relative to the meta box's idat payload
//	2 item offset — not supported: the item is skipped, never mis-resolved

// buildTask254File assembles ftyp + meta{iinf, [idat], iloc v1} + mdat with
// one Exif item (ID 1) and one XMP item (ID 2) whose locations mkIloc
// computes from the absolute offset of the mdat payload.
func buildTask254File(idat, mdat []byte, mkIloc func(mdatData uint64) []byte) []byte {
	var extra [][]byte
	if idat != nil {
		extra = append(extra, bmffBox("idat", idat))
	}
	infes := [][]byte{
		bmffInfeV2(1, "Exif"),
		bmffInfeV2WithContentType(2, "mime", "application/rdf+xml"),
	}
	return buildTask282File(infes, extra, mdat, mkIloc)
}

// TestTask254ConstructionMethods is the conformance gate for the three
// construction methods.
func TestTask254ConstructionMethods(t *testing.T) {
	t.Parallel()
	exif := conformanceMinimalTIFF()
	exifBlock := append([]byte{0, 0, 0, 0}, exif...)
	xmp := conformanceXMPPacket()

	t.Run("method 0 (file offset, mdat)", func(t *testing.T) {
		t.Parallel()
		mdat := append(append([]byte{}, exifBlock...), xmp...)
		data := buildTask254File(nil, mdat, func(d uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 0, extents: [][2]uint64{{d, uint64(len(exifBlock))}}},
				{id: 2, method: 0, extents: [][2]uint64{{d + uint64(len(exifBlock)), uint64(len(xmp))}}},
			})
		})
		assertExtract(t, data, exif, xmp)
	})

	t.Run("method 1 (idat offset)", func(t *testing.T) {
		t.Parallel()
		// Both items live in idat; base_offset applies to idat offsets too.
		idat := append(append([]byte("pad!"), exifBlock...), xmp...)
		data := buildTask254File(idat, []byte("image"), func(uint64) []byte {
			return buildIlocBody(1, 4, 4, 4, 0, []ilocTestEntry{
				{id: 1, method: 1, baseOffset: 4, extents: [][2]uint64{{0, uint64(len(exifBlock))}}},
				{id: 2, method: 1, baseOffset: 4, extents: [][2]uint64{{uint64(len(exifBlock)), uint64(len(xmp))}}},
			})
		})
		assertExtract(t, data, exif, xmp)
	})

	t.Run("method 1 split across idat extents", func(t *testing.T) {
		t.Parallel()
		half := len(exifBlock) / 2
		head, tail := uint64(half), uint64(len(exifBlock)-half) //nolint:gosec // G115: small non-negative test lengths
		idat := append(append(append([]byte{}, exifBlock[half:]...), exifBlock[:half]...), xmp...)
		data := buildTask254File(idat, []byte("image"), func(uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 1, extents: [][2]uint64{
					{tail, head},
					{0, tail},
				}},
				{id: 2, method: 1, extents: [][2]uint64{{uint64(len(exifBlock)), uint64(len(xmp))}}},
			})
		})
		assertExtract(t, data, exif, xmp)
	})

	t.Run("method 1 without an idat box is skipped", func(t *testing.T) {
		t.Parallel()
		data := buildTask254File(nil, append(append([]byte{}, exifBlock...), xmp...), func(uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 1, extents: [][2]uint64{{0, uint64(len(exifBlock))}}},
			})
		})
		assertExtract(t, data, nil, nil)
	})

	t.Run("method 1 extent beyond idat is rejected", func(t *testing.T) {
		t.Parallel()
		data := buildTask254File(exifBlock, []byte("image"), func(uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 1, extents: [][2]uint64{{1, uint64(len(exifBlock))}}},
			})
		})
		assertExtract(t, data, nil, nil)
	})

	t.Run("method 2 (item offset) is skipped", func(t *testing.T) {
		t.Parallel()
		mdat := append(append([]byte{}, exifBlock...), xmp...)
		data := buildTask254File(nil, mdat, func(d uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 2, extents: [][2]uint64{{d, uint64(len(exifBlock))}}},
				{id: 2, method: 2, extents: [][2]uint64{{d + uint64(len(exifBlock)), uint64(len(xmp))}}},
			})
		})
		assertExtract(t, data, nil, nil)
	})

	t.Run("reserved bits do not change the method", func(t *testing.T) {
		t.Parallel()
		// The upper 12 bits of the 16-bit field are reserved (§8.11.3.2);
		// construction_method is the low 4 bits only.
		idat := append([]byte{}, exifBlock...)
		data := buildTask254File(idat, []byte("image"), func(uint64) []byte {
			return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, method: 0xFFF1, extents: [][2]uint64{{0, uint64(len(exifBlock))}}},
			})
		})
		assertExtract(t, data, exif, nil)
	})
}

// assertExtract fails t unless Extract(data) returns exactly wantEXIF and
// wantXMP (nil meaning absent) without error.
func assertExtract(t *testing.T, data, wantEXIF, wantXMP []byte) {
	t.Helper()
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, wantEXIF) || (rawEXIF == nil) != (wantEXIF == nil) {
		t.Errorf("rawEXIF = %d bytes (nil=%v), want %d bytes (nil=%v)", len(rawEXIF), rawEXIF == nil, len(wantEXIF), wantEXIF == nil)
	}
	if !bytes.Equal(rawXMP, wantXMP) || (rawXMP == nil) != (wantXMP == nil) {
		t.Errorf("rawXMP = %d bytes (nil=%v), want %d bytes (nil=%v)", len(rawXMP), rawXMP == nil, len(wantXMP), wantXMP == nil)
	}
}
