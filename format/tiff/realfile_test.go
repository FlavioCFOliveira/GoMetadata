package tiff

// realfile_test.go — regression tests against real TIFF fixtures.
//
// Task #97 bug: gometadata.Write to TIFF/DNG failed on every real file with
// "tiff: enumerate image blocks: image block out of bounds" because the write
// path passed exif.Encode(m.EXIF) (an IFD skeleton without image blocks) as
// the relocation base. This test guards against a regression.
//
// Fixture: format/tiff/testdata/cramps.tif
//   Source: libtiff test suite (https://download.osgeo.org/libtiff/pics/)
//   License: freely distributable — no restrictions on use or redistribution.
//   Content: stripped grayscale TIFF, 800×607, uncompressed (PhotometricInterp=1).
//
// The test does NOT use the top-level gometadata.Write to keep the dependency
// graph clean. Instead it exercises InjectWithEXIF directly with a parsed
// (then mutated) *exif.EXIF struct, which is exactly the path that Write uses.

import (
	"bytes"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

const crampsFixture = "testdata/cramps.tif"

// TestInjectWithEXIFRealFile_CrampsTIF is a regression test for task #97:
// InjectWithEXIF must produce a well-formed TIFF file where:
//  1. The copyright entry written to the *exif.EXIF struct survives in the output.
//  2. All image-block bytes are byte-identical to the source (pixel data preserved).
//  3. The output can be re-parsed by exif.Parse without error.
//
// Implementation note: relocateTIFFFromParsed mutates the *exif.EXIF struct
// in-place (via insertPlaceholders / updatePlaceholders) so that exif.Encode
// writes the correct relocated offsets. Therefore the original strip offsets
// must be recorded BEFORE calling InjectWithEXIF; after the call the struct
// reflects the relocated state.
func TestInjectWithEXIFRealFile_CrampsTIF(t *testing.T) {
	t.Parallel()

	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("testdata/cramps.tif not present; skipping real-file regression test")
		}
		t.Fatalf("read fixture: %v", err)
	}

	// Step 1: parse the original TIFF — same path as gometadata.Read.
	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("exif.Parse original: %v", err)
	}

	// Step 2: record original strip offsets and sizes BEFORE mutation.
	// relocateTIFFFromParsed mutates e.IFD0 in-place (offset entries are
	// replaced with relocated values). We save them now so we can verify that
	// the relocated result bytes match the original strip bytes.
	origStrips := snapshotStrips(t, e)

	// Step 3: set a copyright string — same mutation that SetCopyright performs.
	const wantCopyright = "© 2026 regression-test"
	e.SetCopyright(wantCopyright)

	// Step 4: inject using InjectWithEXIF (original bytes + modified struct).
	// rawIPTC and rawXMP are nil to isolate the EXIF-struct write path.
	var out bytes.Buffer
	if err := InjectWithEXIF(original, e, nil, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIF: %v", err)
	}

	result := out.Bytes()

	// Step 5: re-parse the result.
	e2, err := exif.Parse(result)
	if err != nil {
		t.Fatalf("exif.Parse result: %v", err)
	}

	// Step 6: verify copyright was written.
	got := e2.Copyright()
	if got != wantCopyright {
		t.Errorf("Copyright: got %q, want %q", got, wantCopyright)
	}

	// Step 7: verify image blocks are byte-identical.
	// Compare bytes at each strip's NEW offset (from e2/result) against the
	// original strip bytes (from origStrips / original).
	verifyImageBlocksIdentical(t, original, origStrips, result, e2)
}

// TestInjectWithEXIFRealFile_PassThrough verifies that when modifiedEXIF is nil
// and rawIPTC/rawXMP are nil, InjectWithEXIF writes the original bytes verbatim.
func TestInjectWithEXIFRealFile_PassThrough(t *testing.T) {
	t.Parallel()

	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("testdata/cramps.tif not present; skipping real-file regression test")
		}
		t.Fatalf("read fixture: %v", err)
	}

	var out bytes.Buffer
	if err := InjectWithEXIF(original, nil, nil, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIF pass-through: %v", err)
	}

	if !bytes.Equal(original, out.Bytes()) {
		t.Errorf("pass-through: output differs from input (%d vs %d bytes)",
			len(original), out.Len())
	}
}

// stripRecord records a single strip's original offset and byte count.
type stripRecord struct {
	srcOffset uint32
	length    uint32
}

// snapshotStrips reads the StripOffsets and StripByteCounts from e before any
// mutation by relocateTIFFFromParsed. Returns nil if no strip entries are found.
//
// The snapshot is needed because relocateTIFFFromParsed mutates e.IFD0.Entries
// in-place (insertPlaceholders replaces the offset values with relocated ones).
func snapshotStrips(t *testing.T, e *exif.EXIF) []stripRecord {
	t.Helper()
	if e.IFD0 == nil {
		return nil
	}
	offEntry := e.IFD0.Get(exif.TagStripOffsets)
	cntEntry := e.IFD0.Get(exif.TagStripByteCounts)
	if offEntry == nil || cntEntry == nil || offEntry.Count == 0 {
		return nil
	}
	order := e.ByteOrder
	if order == nil {
		return nil
	}
	n := int(offEntry.Count)
	offSz := int(typeSize(uint16(offEntry.Type)))
	cntSz := int(typeSize(uint16(cntEntry.Type)))
	if offSz == 0 || cntSz == 0 {
		return nil
	}
	if len(offEntry.Value) < n*offSz || len(cntEntry.Value) < n*cntSz {
		return nil
	}
	records := make([]stripRecord, n)
	for i := range n {
		off, err1 := readUint(offEntry.Value[i*offSz:], offSz, order)
		cnt, err2 := readUint(cntEntry.Value[i*cntSz:], cntSz, order)
		if err1 != nil || err2 != nil {
			t.Errorf("snapshotStrips[%d]: %v %v", i, err1, err2)
			return nil
		}
		records[i] = stripRecord{srcOffset: off, length: cnt}
	}
	return records
}

// verifyImageBlocksIdentical compares each strip's bytes between the original
// (at pre-mutation offsets) and the result (at relocated offsets from resIFD).
//
// origStrips: pre-mutation snapshot of original strip offsets and sizes.
// resIFD: parsed EXIF of the result; provides the new relocated offsets.
//
// This is the regression guard for task #97: a broken relocator that skips or
// corrupts image blocks would produce different strip bytes.
func verifyImageBlocksIdentical(t *testing.T, original []byte, origStrips []stripRecord, result []byte, resIFD *exif.EXIF) {
	t.Helper()

	if origStrips == nil {
		t.Skip("no strip snapshot available; skipping image-block check")
		return
	}
	if resIFD.IFD0 == nil {
		t.Error("nil IFD0 in result EXIF")
		return
	}

	order := resIFD.ByteOrder
	if order == nil {
		t.Error("nil ByteOrder in result EXIF")
		return
	}

	resOff := resIFD.IFD0.Get(exif.TagStripOffsets)
	resLen := resIFD.IFD0.Get(exif.TagStripByteCounts)
	if resOff == nil || resLen == nil {
		t.Error("strip-offset entries missing from result IFD0")
		return
	}

	n := len(origStrips)
	if int(resOff.Count) != n {
		t.Errorf("strip count mismatch: original %d, result %d", n, resOff.Count)
		return
	}

	resOffSz := int(typeSize(uint16(resOff.Type)))
	resCntSz := int(typeSize(uint16(resLen.Type)))
	if resOffSz == 0 || resCntSz == 0 {
		t.Error("result strip entries have unknown type")
		return
	}
	if len(resOff.Value) < n*resOffSz || len(resLen.Value) < n*resCntSz {
		t.Error("result strip arrays too short")
		return
	}

	for i := range n {
		orig := origStrips[i]

		resNewOff, err3 := readUint(resOff.Value[i*resOffSz:], resOffSz, order)
		resSrcLen, err4 := readUint(resLen.Value[i*resCntSz:], resCntSz, order)
		if err3 != nil || err4 != nil {
			t.Errorf("strip[%d]: read result offset/length: %v %v", i, err3, err4)
			continue
		}

		if orig.length != resSrcLen {
			t.Errorf("strip[%d]: length mismatch: original %d, result %d", i, orig.length, resSrcLen)
			continue
		}
		if orig.length == 0 {
			continue // zero-size strip; nothing to compare
		}

		end1 := uint64(orig.srcOffset) + uint64(orig.length)
		end2 := uint64(resNewOff) + uint64(resSrcLen)
		if end1 > uint64(len(original)) {
			t.Errorf("strip[%d]: original offset %d+%d out of bounds (file len %d)",
				i, orig.srcOffset, orig.length, len(original))
			continue
		}
		if end2 > uint64(len(result)) {
			t.Errorf("strip[%d]: result offset %d+%d out of bounds (file len %d)",
				i, resNewOff, resSrcLen, len(result))
			continue
		}
		origBytes := original[orig.srcOffset:end1]
		resBytes := result[resNewOff:end2]
		if !bytes.Equal(origBytes, resBytes) {
			t.Errorf("strip[%d]: image bytes differ (orig offset=%d result offset=%d length=%d)",
				i, orig.srcOffset, resNewOff, orig.length)
		}
	}
}
