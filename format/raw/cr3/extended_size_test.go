package cr3

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// CR3-EXTSIZE-01 regression battery.
//
// findMoovRange / flatUUIDBoxRange already resolved the real size of an
// ISOBMFF box that uses the extended box-size encoding (ISO 14496-12 §4.2:
// 32-bit size field == 1, followed by an 8-byte largesize, making the header
// 16 bytes instead of 8). The bug was that the WRITE path
// (injectIntoMoov / rebuildMoovContent) hardcoded the normal 8-byte header
// length when slicing box content, silently corrupting the box tree whenever
// the source moov box or the Canon uuid box used extended encoding.
//
// buildExtendedBox and buildExtendedUUIDBox below construct spec-legal
// extended-size boxes; they are kept as reusable, exported-within-package
// helpers so a future FuzzCR3Inject corpus can seed from the same byte
// sequences these tests exercise.

// buildExtendedBox constructs an ISOBMFF box using the EXTENDED size encoding:
//
//	[4-byte size==1][4-byte type][8-byte largesize][content]
//
// ISO 14496-12 §4.2: this form is used when the box's true size must be
// carried in 64 bits; the normal 4-byte size field is set to the sentinel
// value 1 to signal that largesize follows immediately after the type.
func buildExtendedBox(boxType string, content []byte) []byte {
	total := 16 + len(content)
	box := make([]byte, total)
	binary.BigEndian.PutUint32(box[0:4], 1) // size==1 → largesize follows
	copy(box[4:8], boxType)
	binary.BigEndian.PutUint64(box[8:16], uint64(total))
	copy(box[16:], content)
	return box
}

// buildExtendedUUIDBox constructs a uuid box using the EXTENDED size encoding:
//
//	[4-byte size==1]["uuid"][8-byte largesize][16-byte UUID][content]
func buildExtendedUUIDBox(uuid, content []byte) []byte {
	total := 16 + 16 + len(content)
	box := make([]byte, total)
	binary.BigEndian.PutUint32(box[0:4], 1) // size==1 → largesize follows
	copy(box[4:8], "uuid")
	binary.BigEndian.PutUint64(box[8:16], uint64(total))
	copy(box[16:32], uuid)
	copy(box[32:], content)
	return box
}

// ftypBox16 is the minimal 16-byte "crx " ftyp box shared by buildMinimalCR3
// and every test in this file.
func ftypBox16() []byte {
	return []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0}
}

// TestCR3InjectExtendedSizeMoovBoxPreservesEXIF is the regression gate for
// CR3-EXTSIZE-01, variant A: the source moov box itself uses the extended
// box-size encoding.
//
// Before the fix, injectIntoMoov sliced moovContent at data[moovStart+8:],
// eight bytes short of the true 16-byte extended header. That embedded the
// last 8 bytes of the largesize field as bogus leading "content" inside the
// (misidentified) Canon uuid box, so the CMT1 replacement was written into a
// corrupted structure: Inject returned no error, but the new EXIF never
// reached a position a subsequent Extract could find, and Extract on the
// output failed with ErrNoCMT1Box.
func TestCR3InjectExtendedSizeMoovBoxPreservesEXIF(t *testing.T) {
	t.Parallel()

	origExif := minimalTIFF()
	newExif := append(minimalTIFF(), 0xAA, 0xBB, 0xCC) // different size to force a moov-size delta

	// uuid (normal) → CMT1 (normal).
	cmt1Box := buildBox("CMT1", origExif)
	uuidBox := buildUUIDBox(canonUUID, cmt1Box)

	// moov (EXTENDED) wraps the normal uuid box.
	extendedMoov := buildExtendedBox("moov", uuidBox)

	data := append(ftypBox16(), extendedMoov...)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil, true); err != nil {
		t.Fatalf("Inject with extended-size moov box: unexpected error: %v", err)
	}

	// The new EXIF must be present verbatim in the output bytes.
	if !bytes.Contains(out.Bytes(), newExif) {
		t.Error("Inject output does not contain the new EXIF bytes — CR3-EXTSIZE-01 regression")
	}

	// A subsequent Extract must succeed and return the new EXIF, not an
	// ErrNoCMT1Box or the stale original EXIF.
	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject with extended-size moov box: unexpected error: %v (CR3-EXTSIZE-01 regression)", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("Extract after Inject: rawEXIF = %x, want %x (new EXIF must replace, not be discarded)", rawEXIF, newExif)
	}
	if bytes.Equal(rawEXIF, origExif) {
		t.Error("Extract after Inject: rawEXIF still equals the ORIGINAL EXIF — new EXIF was silently discarded")
	}
}

// TestCR3InjectExtendedSizeUUIDBoxPreservesSiblings is the regression gate for
// CR3-EXTSIZE-01, variant B: a normal-size moov box wraps a Canon uuid box
// that itself uses the extended box-size encoding.
//
// Before the fix, rebuildMoovContent sliced the uuid payload at
// moovContent[uuidStart+24:] (the hardcoded 8-byte-header + 16-byte-UUID
// offset), eight bytes short of the true 16-byte-header + 16-byte-UUID
// offset. That misaligned every subsequent sub-box boundary inside the uuid
// content, so rebuildUUIDContent's box-by-box scan desynchronised and its
// sibling sub-boxes — CMT2 (the Exif IFD referenced by CMT1's ExifIFD
// pointer) and the pre-existing "XMP " packet — were silently dropped from
// the rebuilt content, even though the caller passed rawXMP=nil intending to
// preserve them (CLAUDE.md §5: writes must preserve all metadata not
// explicitly modified).
func TestCR3InjectExtendedSizeUUIDBoxPreservesSiblings(t *testing.T) {
	t.Parallel()

	origExif := minimalTIFF()
	newExif := append(minimalTIFF(), 0x11, 0x22) // different size to force a moov-size delta

	cmt2Payload := []byte("CMT2-Exif-IFD-payload-bytes-preserved-verbatim")
	origXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)

	cmt1Box := buildBox("CMT1", origExif)
	cmt2Box := buildBox("CMT2", cmt2Payload)
	xmpBox := buildBox("XMP ", origXMP)

	// Canon uuid content: CMT1 + CMT2 + XMP , concatenated as sibling boxes.
	uuidContent := append(append(append([]byte{}, cmt1Box...), cmt2Box...), xmpBox...)

	// uuid (EXTENDED) wraps the sibling sub-boxes.
	extendedUUID := buildExtendedUUIDBox(canonUUID, uuidContent)

	// moov (normal) wraps the extended uuid box.
	moovBox := buildBox("moov", extendedUUID)

	data := append(ftypBox16(), moovBox...)

	var out bytes.Buffer
	// rawXMP=nil signals "preserve existing XMP unmodified".
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil, true); err != nil {
		t.Fatalf("Inject with extended-size uuid box: unexpected error: %v", err)
	}
	outBytes := out.Bytes()

	// The new EXIF must be present.
	if !bytes.Contains(outBytes, newExif) {
		t.Error("Inject output does not contain the new EXIF bytes")
	}

	// CMT2 must survive byte-for-byte: Inject never rebuilds CMT2, it only
	// replaces CMT1 and (optionally) XMP , so the CMT2 box bytes emitted by
	// rebuildUUIDContent's "copy unchanged" default case must be identical to
	// the original.
	if !bytes.Contains(outBytes, cmt2Box) {
		t.Error("CMT2 sibling sub-box was dropped from Inject output — CR3-EXTSIZE-01 regression (metadata not preserved)")
	}

	// XMP  was passed as nil (preserve): the original XMP box must survive
	// byte-for-byte.
	if !bytes.Contains(outBytes, xmpBox) {
		t.Error("XMP  sibling sub-box was dropped from Inject output — CR3-EXTSIZE-01 regression (metadata not preserved)")
	}

	// Round-trip through Extract: new EXIF must be returned without error.
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatalf("Extract after Inject with extended-size uuid box: unexpected error: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("Extract after Inject: rawEXIF = %x, want %x", rawEXIF, newExif)
	}
	if !bytes.Equal(rawXMP, origXMP) {
		t.Errorf("Extract after Inject: rawXMP = %q, want original %q (XMP must be preserved when rawXMP=nil)", rawXMP, origXMP)
	}
}
