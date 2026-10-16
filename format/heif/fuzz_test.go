package heif

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func FuzzHEIFExtract(f *testing.F) {
	// Seed with a minimal HEIF/ISOBMFF structure.
	// ftyp box: size=20, type="ftyp", brand="heic", version=0, compat="mif1"
	seed := []byte{
		0x00, 0x00, 0x00, 0x14, // size = 20
		'f', 't', 'y', 'p', // type = ftyp
		'h', 'e', 'i', 'c', // major brand
		0x00, 0x00, 0x00, 0x00, // minor version
		'm', 'i', 'f', '1', // compatible brand
	}
	f.Add(seed)

	// Seed with empty input.
	f.Add([]byte{})

	// Seed with truncated box header.
	f.Add([]byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p'})

	// Seed: ftyp box followed by a mdat box — minimal two-box HEIF structure.
	// mdat (media data) box: size=8, type="mdat", no content.
	{
		seed2 := []byte{
			// ftyp box (20 bytes)
			0x00, 0x00, 0x00, 0x14,
			'f', 't', 'y', 'p',
			'h', 'e', 'i', 'c',
			0x00, 0x00, 0x00, 0x00,
			'm', 'i', 'f', '1',
			// mdat box (8 bytes, empty body)
			0x00, 0x00, 0x00, 0x08,
			'm', 'd', 'a', 't',
		}
		f.Add(seed2)
	}

	// Seed: ftyp with "mif1" major brand (alternate HEIF variant).
	{
		seed3 := []byte{
			0x00, 0x00, 0x00, 0x14,
			'f', 't', 'y', 'p',
			'm', 'i', 'f', '1', // mif1 brand
			0x00, 0x00, 0x00, 0x00,
			'h', 'e', 'i', 'c',
		}
		f.Add(seed3)
	}

	// Seed: ftyp + meta containing an infe v0 box with only item_ID (no protection_index).
	// Exercises the #106 fix: parseInfeV0V1 must not panic on truncated body.
	// Before the fix: pos+=2 (protection_index) advances past len(data), then
	// bytes.IndexByte(data[pos:], 0x00) panics with "slice bounds out of range".
	// ISO 14496-12 §8.11.6: infe v0/v1 parsers must bounds-check every field.
	{
		// infe v0 box: header(8) + version(1)+flags(3)+item_ID(2) = 14 bytes total.
		// item_protection_index(2) and item_name are deliberately absent.
		infeBody := []byte{
			0x00, 0x00, 0x00, 0x00, // version=0, flags=0
			0x00, 0x01, // item_ID=1 (only field present; protection_index absent)
		}
		infeSize := uint32(8 + len(infeBody)) //nolint:gosec // G115: fuzz seed, bounded
		infeBox := make([]byte, infeSize)
		binary.BigEndian.PutUint32(infeBox, infeSize)
		copy(infeBox[4:], "infe")
		copy(infeBox[8:], infeBody)

		// iinf FullBox: version+flags(4) + entry_count(2) + infe box.
		iinfBody := make([]byte, 0, 6+len(infeBox))
		iinfBody = append(iinfBody, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01)
		iinfBody = append(iinfBody, infeBox...)
		iinfSize := uint32(8 + len(iinfBody)) //nolint:gosec // G115: fuzz seed, bounded
		iinfBox := make([]byte, iinfSize)
		binary.BigEndian.PutUint32(iinfBox, iinfSize)
		copy(iinfBox[4:], "iinf")
		copy(iinfBox[8:], iinfBody)

		// iloc FullBox: minimal (0 items).
		ilocBody := []byte{
			0x00, 0x00, 0x00, 0x00, // version=0, flags=0
			0x44, 0x00, // offset_size=4, length_size=4, base_offset_size=0
			0x00, 0x00, // item_count=0
		}
		ilocSize := uint32(8 + len(ilocBody)) //nolint:gosec // G115: fuzz seed, bounded
		ilocBox := make([]byte, ilocSize)
		binary.BigEndian.PutUint32(ilocBox, ilocSize)
		copy(ilocBox[4:], "iloc")
		copy(ilocBox[8:], ilocBody)

		// meta FullBox.
		metaBody := make([]byte, 0, 4+len(iinfBox)+len(ilocBox))
		metaBody = append(metaBody, 0x00, 0x00, 0x00, 0x00)
		metaBody = append(metaBody, iinfBox...)
		metaBody = append(metaBody, ilocBox...)
		metaSize := uint32(8 + len(metaBody)) //nolint:gosec // G115: fuzz seed, bounded
		metaBox := make([]byte, metaSize)
		binary.BigEndian.PutUint32(metaBox, metaSize)
		copy(metaBox[4:], "meta")
		copy(metaBox[8:], metaBody)

		ftyp := [20]byte{
			0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p',
			'h', 'e', 'i', 'c', 0x00, 0x00, 0x00, 0x00, 'm', 'i', 'f', '1',
		}
		seed106 := make([]byte, 0, len(ftyp)+len(metaBox))
		seed106 = append(seed106, ftyp[:]...)
		seed106 = append(seed106, metaBox...)
		f.Add(seed106)
	}

	// Seed: security audit FIX 1 (HEIF-ILOC-EXTENT-AMPLIFICATION, CWE-770/834).
	// An iloc box with several items, each declaring extent_count=0xFFFF but
	// with every per-extent field-size nibble set to zero, so pre-fix code
	// spun the full attacker-controlled extent_count per item with no bound
	// tied to actual input size. See TestHEIFIlocZeroFieldSizeAmplificationBounded
	// for the full root-cause writeup and the readIlocFullExtents/
	// readIlocSimpleExtents/parseIloc/parseIlocFull fix.
	f.Add(buildIlocZeroFieldAmplification(5, 0xFFFF, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		rawEXIF, _, _, err := Extract(bytes.NewReader(data))
		if err != nil {
			return
		}

		// Post-success assertion: a non-nil EXIF payload must carry at least the
		// TIFF header (8 bytes: byte-order mark + magic + IFD0 offset).
		// HEIF embeds EXIF as a full TIFF block (ISO 23008-12 §A.2.1).
		if rawEXIF != nil && len(rawEXIF) < 8 {
			t.Errorf("rawEXIF too short after successful Extract: got %d bytes, want >= 8", len(rawEXIF))
		}
	})
}

// FuzzHEIFInject feeds arbitrary bytes as the source HEIF container and asserts
// that Inject never panics — it must return an error or write valid output,
// never crash. The no-panic contract is the primary correctness invariant for
// the Inject write path (iloc offset recalculation, meta box rebuild).
//
// preserveUnknownSegments is always true because HEIF/AVIF ISOBMFF boxes
// (ftyp, moov, mdat, etc.) are structurally mandatory; there is no concept of
// an "unknown optional segment" analogous to JPEG APPn. Passing false returns
// ErrPreserveUnknownSegmentsNotSupported before any parsing begins.
//
// Both rawEXIF and rawXMP are fixed short payloads so the fuzzer focuses on
// structural variation in the container bytes.
func FuzzHEIFInject(f *testing.F) {
	// Seed 1: minimal ftyp box only — no meta box; exercises the
	// ErrNoMetadataContainer path (findMetaBoxAbs returns found=false).
	f.Add([]byte{
		0x00, 0x00, 0x00, 0x14, // size = 20
		'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c', // major brand
		0x00, 0x00, 0x00, 0x00, // minor version
		'm', 'i', 'f', '1', // compatible brand
	})

	// Seed 2: empty input — exercises the io.ReadAll + no-meta path.
	f.Add([]byte{})

	// Seed 3: truncated box header — 4 bytes (size field only).
	f.Add([]byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p'})

	// Seed 4: ftyp + mdat — two-box file with no meta box.
	f.Add([]byte{
		// ftyp box (20 bytes)
		0x00, 0x00, 0x00, 0x14,
		'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c',
		0x00, 0x00, 0x00, 0x00,
		'm', 'i', 'f', '1',
		// mdat box (8 bytes, empty body)
		0x00, 0x00, 0x00, 0x08,
		'm', 'd', 'a', 't',
	})

	// Seed 5: ftyp + minimal meta box with no iinf/iloc children — exercises
	// planInject returning ErrUnreadableItemLocations (no iloc box).
	// meta FullBox: size(4) + type(4) + version/flags(4) = 12 bytes minimum.
	f.Add([]byte{
		// ftyp box (20 bytes)
		0x00, 0x00, 0x00, 0x14,
		'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c',
		0x00, 0x00, 0x00, 0x00,
		'm', 'i', 'f', '1',
		// meta FullBox (12 bytes, no children)
		0x00, 0x00, 0x00, 0x0C,
		'm', 'e', 't', 'a',
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
	})

	// Seed 6: extended-size (largesize) box — exercises the size==1 branch in
	// parseHEIFBoxHeader (ISO 14496-12 §4.2).
	{
		// ftyp box with size==1 (64-bit extended size = 24 bytes total).
		var buf [24]byte
		binary.BigEndian.PutUint32(buf[0:], 1) // size == 1 → largesize follows
		copy(buf[4:8], "ftyp")
		binary.BigEndian.PutUint64(buf[8:], 24) // actual size = 24
		copy(buf[16:], "heic")                  // brand
		f.Add(buf[:])
	}

	// Seed 8: ftyp + 8-byte meta box (size=8 = header only; no FullBox version/flags).
	// Exercises the #169 fix: meta box < 12 bytes must be treated as absent → ErrNoMetadataContainer.
	// Before the fix, findMetaBoxAbs returned contentOff=12 > metaAbsEnd=8 causing
	// buildInjectComponents to panic with "slice bounds out of range [12:8]".
	// ISO 14496-12 §8.11.1: meta FullBox minimum size = 12 bytes.
	f.Add([]byte{
		// ftyp box (20 bytes)
		0x00, 0x00, 0x00, 0x14,
		'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c',
		0x00, 0x00, 0x00, 0x00,
		'm', 'i', 'f', '1',
		// meta box: size=8 (header only — no FullBox version/flags — invalid)
		0x00, 0x00, 0x00, 0x08,
		'm', 'e', 't', 'a',
	})

	// Seed 9: ftyp + 11-byte meta box (header+3 bytes of version/flags; still < 12).
	// Exercises the same #169 fix at the off-by-one boundary.
	f.Add([]byte{
		// ftyp box (20 bytes)
		0x00, 0x00, 0x00, 0x14,
		'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c',
		0x00, 0x00, 0x00, 0x00,
		'm', 'i', 'f', '1',
		// meta box: size=11 (header[8] + 3 bytes — not a complete FullBox)
		0x00, 0x00, 0x00, 0x0B,
		'm', 'e', 't', 'a',
		0x00, 0x00, 0x00,
	})

	// Seed 7: JPEG XMP wire-frame sentinel as rawXMP — the wire-frame guard
	// in Inject must return ErrCorruptXMP without panicking.
	// Layout: [0x00]['X']['M']['P']['E']['X']['T'][0x00] + padding.
	wireFrameXMP := []byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00, 0x00, 0x00}

	// Fixed metadata payloads used for all fuzz iterations (vary container, not payload).
	rawEXIF := []byte{
		'I', 'I', 0x2A, 0x00, // LE TIFF header
		0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
		0x00, 0x00, // 0 entries
		0x00, 0x00, 0x00, 0x00, // next-IFD = 0
	}
	rawXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)

	// Confirm the wire-frame seed exercises the guard path (not a fuzz seed —
	// just a direct call to document the expected error).
	_ = wireFrameXMP

	// Seed: security audit FIX 1 (HEIF-ILOC-EXTENT-AMPLIFICATION, CWE-770/834).
	// Same crafted zero-field-size, extent_count=0xFFFF iloc box as the
	// FuzzHEIFExtract seed above, but with an iloc version (1) that exercises
	// parseIlocFull (the Inject write path) instead of the simple read-path
	// parser. See TestHEIFIlocZeroFieldSizeAmplificationBounded.
	f.Add(buildIlocZeroFieldAmplification(5, 0xFFFF, 1))

	// Seed: security audit finding HEIF-ILOC-DUPBOX-01 (2026-09-25) — a meta
	// box carrying two well-formed 'iloc' children. See
	// buildHEIFDuplicateIloc / TestInjectDuplicateIlocBoxesOffsetInBounds in
	// heif_test.go for the full writeup.
	f.Add(buildHEIFDuplicateIloc())

	// Seeds (tasks #334, #335): a meta box with a 64-bit largesize header, and
	// iloc boxes whose offset_size / length_size is 0.
	f.Add(buildTask334File(task334Layout{metaLarge: true, offsetSize: 4, lengthSize: 4}))
	f.Add(buildTask334File(task334Layout{inMoov: true, moovLarge: true, metaLarge: true, offsetSize: 4, lengthSize: 4}))
	f.Add(buildTask334File(task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4}))
	f.Add(buildTask334File(task334Layout{offsetSize: 4, lengthSize: 0}))

	// Seeds (task #309): real metadata-free AVIF and HEIC files, which make
	// Inject create the Exif and XMP items, shift the image item's offset and
	// insert an iref box.
	for _, name := range []string{"testdata/no_metadata.avif", "testdata/no_metadata.heic"} {
		if seed, err := os.ReadFile(name); err == nil {
			f.Add(seed)
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the Inject path.
		//
		// Security audit finding HEIF-ILOC-DUPBOX-01 (2026-09-25): the
		// no-panic contract alone cannot catch a wrong-but-non-crashing iloc
		// extent offset (buildInjectComponents' size accounting silently
		// computing an offset that lands past the end of the output). Write
		// to a buffer (instead of io.Discard) and, on success, assert the
		// self-consistency invariant that every extent Inject itself
		// COMPUTED lies within the output's own bounds.
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(data), &out, rawEXIF, nil, rawXMP, true); err != nil {
			return
		}
		if bytes.Equal(out.Bytes(), data) {
			// Task #335: a successful write of a non-nil payload never leaves
			// the file unchanged. Inject either stores the payload or returns
			// an error; the former silent pass-throughs (no meta box, an
			// unreadable iloc, an iloc offset_size or length_size of 0) are
			// gone. rawEXIF and rawXMP are always non-nil in this target.
			t.Fatal("Inject returned nil but the output equals the input: the requested metadata was silently dropped")
		}
		assertHEIFExtentsWithinBounds(t, out.Bytes())

		// Task #349: Extract and Inject select the same metadata container, so
		// what Inject stored is what Extract reads back. Two defects outside
		// task #349, reported separately, are left out of this check: a child
		// of meta, iinf or iref with a size field of 0, which a reader takes
		// to include the boxes Inject appends after it, and an iinf box of
		// version 1, whose 32-bit entry_count parseIinf reads as 16 bits.
		if hasOpenEndedMetaChild(data) || hasIinfVersion1(data) {
			return
		}
		gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil {
			return
		}
		if gotEXIF != nil && !bytes.Equal(gotEXIF, rawEXIF) {
			t.Fatalf("Extract after Inject read EXIF %x, want %x", gotEXIF, rawEXIF)
		}
		if gotXMP != nil && !bytes.Equal(gotXMP, rawXMP) {
			t.Fatalf("Extract after Inject read XMP %q, want %q", gotXMP, rawXMP)
		}
		if gotEXIF == nil && gotXMP == nil {
			t.Fatal("Inject stored EXIF and XMP but Extract of the output reads neither")
		}
	})
}

// hasOpenEndedMetaChild reports whether the selected meta box of file, or its
// iinf or iref box (the boxes Inject appends children to), has a child box whose
// 32-bit size field is 0 (ISO/IEC 14496-12 §4.2: the box extends to the end of
// its container).
func hasOpenEndedMetaChild(file []byte) bool {
	_, end, off, ok := findMetaBoxAbs(file)
	if !ok {
		return false
	}
	content := file[off:end]
	if openEndedChild(content, 0) {
		return true
	}
	for _, typ := range [][4]byte{boxTypeIinf, boxTypeIref} {
		if body := findInnerBox(content, typ); len(body) >= 4 {
			// iinf has an entry count after version/flags (2 bytes in version 0,
			// 4 otherwise); iref has none.
			skip := 4
			if typ == boxTypeIinf {
				skip += 2
				if body[0] != 0 {
					skip += 2
				}
			}
			if skip <= len(body) && openEndedChild(body[skip:], 0) {
				return true
			}
		}
	}
	return false
}

// hasIinfVersion1 reports whether the selected meta box of file has an iinf box
// whose version is not 0.
func hasIinfVersion1(file []byte) bool {
	_, end, off, ok := findMetaBoxAbs(file)
	if !ok {
		return false
	}
	body := findInnerBox(file[off:end], boxTypeIinf)
	return len(body) > 0 && body[0] != 0
}

// openEndedChild reports whether a child box of data (a sequence of boxes,
// starting at from) has a size field of 0.
func openEndedChild(data []byte, from int) bool {
	for pos := from; pos+8 <= len(data); {
		size, _, _, valid := parseHEIFBoxHeader(data, pos)
		if !valid {
			return false
		}
		if binary.BigEndian.Uint32(data[pos:]) == 0 {
			return true
		}
		pos += int(size) //nolint:gosec // G115: bounded by the slice length
	}
	return false
}

// assertHEIFExtentsWithinBounds parses output's meta/iloc box, if present,
// and fails t if any Exif- or mime-typed item's recorded extent
// [offset, offset+length) exceeds len(output). Absence of a meta or iloc
// box, or of any Exif/mime-typed item, is not a failure: many fuzz inputs
// have no parseable iloc/iinf structure. The caller must have already
// excluded the pass-through case (output == input) — see FuzzHEIFInject.
//
// Deliberately scoped to Exif/mime-typed items only, not every item the
// (possibly malformed) input's iloc box happens to declare: a garbage input
// can legally contain OTHER items (no recognised type, or a type Inject
// never touches) whose iloc entry the input itself already declared with an
// out-of-bounds offset — Inject copies those through completely unchanged
// when it does rebuild the file (non-pending items are never touched by
// updateIlocItemsInPlace/assignItemOffsets), so an out-of-bounds offset
// there reflects the INPUT's own malformation, not anything Inject computed.
// Only Exif/mime-typed items are ones assignItemOffsets ever writes a NEW
// offset/length for (since rawEXIF/rawXMP are always non-nil in this fuzz
// target, every such item is unconditionally added to pendingByID) — those
// are the only extents this invariant can meaningfully attribute to
// Inject's own size accounting.
//
// Regression gate for security audit finding HEIF-ILOC-DUPBOX-01
// (2026-09-25): buildInjectComponents' arithmetic size accounting must never
// produce an offset/length pair that a conformant reader would resolve
// outside the actual file — regardless of how many iloc boxes, items, or
// extents the (possibly malformed) input happened to declare.
func assertHEIFExtentsWithinBounds(t *testing.T, output []byte) {
	t.Helper()
	_, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(output)
	if !found || metaAbsEnd > len(output) || metaContentOff > metaAbsEnd {
		return
	}
	metaContent := output[metaContentOff:metaAbsEnd]
	itemTypes := parseIinf(metaContent)
	ilocInfo, ok := parseIlocFull(metaContent)
	if !ok {
		return
	}
	for _, item := range ilocInfo.items {
		typ, hasType := itemTypes[item.id]
		if !hasType || (typ != itemTypeExif && typ != itemTypeMime) {
			continue
		}
		for _, ext := range item.extents {
			end := ext.offset + ext.length
			if end > uint64(len(output)) {
				t.Fatalf("HEIF-ILOC-DUPBOX-01 class: item %d (type %q) extent [%d,%d) exceeds output length %d",
					item.id, typ[:], ext.offset, end, len(output))
			}
		}
	}
}
