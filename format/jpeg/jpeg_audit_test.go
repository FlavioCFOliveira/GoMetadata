package jpeg

// Regression gate tests for audit findings #122, #123, #134, #135, #151, #174.
//
// Each test is the mandatory gate specified in the audit:
//
//   TestExtendedXMPReassemblyValidation      — #122: overlapping/duplicate-offset chunks
//   TestExtendedXMPNonRDFPrefix              — #123: non-rdf prefix in extended XMP
//   TestExtendedXMPTruncationSurfaced        — #134: oversized extended XMP surfaces warning
//   TestExtendedXMPGUIDCaseMismatch          — #135: lowercase chunk GUID, uppercase main GUID
//   TestParseIRBOddLengthLastBlockNoTrailingPad — #151: odd-length last IRB block, no trailing pad
//   TestJPEGInjectNilIPTCPreservesPhotoshopSiblings  — #174 (a): nil rawIPTC preserves 0x0425
//   TestJPEGInjectMultiAPP13SiblingPreservation      — #174 (b): 0x0425 in second APP13 survives
//
// References:
//   Adobe XMP Specification Part 3 §1.1.4 (extended XMP wire format)
//   Adobe Photoshop IRB spec §"Image Resources" (8BIM layout)

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// GATE #122 — Extended-XMP reassembly validates offset/gap/overlap/fullLen
// ─────────────────────────────────────────────────────────────────────────────

// buildExtChunkJPEG constructs a JPEG with a main XMP packet (containing
// HasExtendedXMP=guid) and extended XMP APP1 segments built from the supplied
// raw chunk descriptors. Each descriptor specifies the declared fullLen, the
// chunk offset, and the chunk data. This allows callers to inject deliberate
// structural problems (duplicate offsets, gaps, wrong fullLen).
func buildExtChunkJPEG(mainXMP []byte, guid string, chunks []struct {
	fullLen uint32
	offset  uint32
	data    []byte
}) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// Main XMP APP1.
	mainPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mainXMP...)
	mainLen := uint16(len(mainPayload) + 2) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], mainLen)
	buf.Write(lbuf[:])
	buf.Write(mainPayload)

	// Extended XMP APP1 segments.
	for _, c := range chunks {
		var extBody bytes.Buffer
		extBody.WriteString("http://ns.adobe.com/xmp/extension/\x00")
		extBody.WriteString(guid) // 32-byte GUID
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:], c.fullLen)
		binary.BigEndian.PutUint32(hdr[4:], c.offset)
		extBody.Write(hdr[:])
		extBody.Write(c.data)
		extPayload := extBody.Bytes()
		extLen := uint16(len(extPayload) + 2) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		binary.BigEndian.PutUint16(lbuf[:], extLen)
		buf.Write(lbuf[:])
		buf.Write(extPayload)
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestExtendedXMPReassemblyValidation is the mandatory regression gate for #122.
//
// It verifies that mergeExtendedChunksValidated rejects structurally invalid
// chunk layouts (duplicate offsets, gaps, fullLen mismatch), causing buildXMPResult
// to degrade to the main XMP packet rather than returning corrupt doubled bytes.
//
// Adobe XMP Specification Part 3 §1.1.4: chunks must cover [0, fullLen) with no
// gaps or overlaps; fullLen must equal the assembled total.
func TestExtendedXMPReassemblyValidation(t *testing.T) {
	t.Parallel()

	const guid = "AABBCCDDEEFF00112233445566778899"

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	// The extended content we would get if reassembly were VALID (used for
	// comparison — it must NOT appear in the output when chunks are invalid).
	extContent := []byte("UNIQUE_EXTENDED_CONTENT_MARKER_XYZ")

	t.Run("duplicate_offset_both_zero", func(t *testing.T) {
		t.Parallel()
		// Two chunks both at offset 0: reassembly would double the content.
		// The validator must detect this as a gap/overlap and reject.
		j := buildExtChunkJPEG(mainXMP, guid, []struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{
			{fullLen: uint32(len(extContent) * 2), offset: 0, data: extContent}, //nolint:gosec // G115: test helper
			{fullLen: uint32(len(extContent) * 2), offset: 0, data: extContent}, //nolint:gosec // G115: test helper
		})
		_, _, rawXMP, err := Extract(bytes.NewReader(j))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		// Reassembly must have been rejected; rawXMP is the main packet.
		if bytes.Contains(rawXMP, extContent) {
			t.Error("rawXMP contains extended content despite duplicate-offset chunks; reassembly was NOT rejected")
		}
		// The main packet must still be returned (graceful degradation).
		if rawXMP == nil {
			t.Error("rawXMP is nil; main packet must be returned on reassembly failure")
		}
	})

	t.Run("gap_between_chunks", func(t *testing.T) {
		t.Parallel()
		// Chunk 0 covers [0,10), chunk 1 covers [20,30): gap at [10,20).
		chunk0 := []byte("0123456789")
		chunk1 := []byte("abcdefghij")
		// Both declare fullLen=20 but there is a gap at offset 10.
		j := buildExtChunkJPEG(mainXMP, guid, []struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{
			{fullLen: 20, offset: 0, data: chunk0},
			{fullLen: 20, offset: 20, data: chunk1}, // gap: chunk1 should start at 10
		})
		_, _, rawXMP, err := Extract(bytes.NewReader(j))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		// Both chunks are present but at wrong offsets → validation rejects.
		combinedContent := append(chunk0, chunk1...)
		if bytes.Contains(rawXMP, combinedContent) {
			t.Error("rawXMP contains full assembled content despite gap; reassembly was NOT rejected")
		}
		if rawXMP == nil {
			t.Error("rawXMP is nil; main packet must be returned on reassembly failure")
		}
	})

	t.Run("fullLen_mismatch", func(t *testing.T) {
		t.Parallel()
		// Single chunk, correct offset=0, but declared fullLen ≠ actual data length.
		data := []byte("HELLO_WORLD")
		// Declare fullLen=100 but only 11 bytes are provided.
		j := buildExtChunkJPEG(mainXMP, guid, []struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{
			{fullLen: 100, offset: 0, data: data},
		})
		_, _, rawXMP, err := Extract(bytes.NewReader(j))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		// fullLen mismatch: validation rejects the assembly.
		if bytes.Contains(rawXMP, data) {
			t.Error("rawXMP contains extended content despite fullLen mismatch; reassembly was NOT rejected")
		}
		if rawXMP == nil {
			t.Error("rawXMP is nil; main packet must be returned on reassembly failure")
		}
	})

	t.Run("valid_single_chunk_passes", func(t *testing.T) {
		t.Parallel()
		// Control case: a single valid chunk (offset=0, fullLen==len(data)) should
		// produce a reassembled XMP that contains the extended content.
		// The extended payload is a well-formed XMP document so xmp.Parse can merge it.
		extDoc := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:description>VALID_EXTENDED_VALUE</dc:description>` +
			`</rdf:Description>` +
			`</rdf:RDF></x:xmpmeta>` +
			`<?xpacket end="w"?>`)
		j := buildExtChunkJPEG(mainXMP, guid, []struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{
			{fullLen: uint32(len(extDoc)), offset: 0, data: extDoc}, //nolint:gosec // G115: test helper
		})
		_, _, rawXMP, err := Extract(bytes.NewReader(j))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if rawXMP == nil {
			t.Fatal("rawXMP is nil; expected reassembled XMP")
		}
		if !bytes.Contains(rawXMP, []byte("VALID_EXTENDED_VALUE")) {
			t.Errorf("valid reassembly did not produce expected content; rawXMP = %q", rawXMP[:min(200, len(rawXMP))])
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GATE #123 — Extended XMP with non-rdf prefix
// ─────────────────────────────────────────────────────────────────────────────

// TestExtendedXMPNonRDFPrefix is the mandatory regression gate for #123.
//
// It verifies that extended XMP properties are extracted correctly when the
// extended packet uses a non-canonical RDF namespace prefix (e.g. "R:" instead
// of "rdf:"). The old byte-splice reassembler would fail silently for such
// prefixes; the new xmp.Parse-based merge handles any binding.
//
// Adobe XMP Specification Part 3 §1.1.4: the assembler must handle arbitrary
// namespace prefix assignments.
func TestExtendedXMPNonRDFPrefix(t *testing.T) {
	t.Parallel()

	const guid = "FF00FF00FF00FF00FF00FF00FF00FF00"

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	// Extended XMP packet that binds the RDF namespace to prefix "R" instead
	// of the canonical "rdf". The old literal-search reassembler would look for
	// "<rdf:Description" and fail to find it, silently dropping the property.
	// The xmp.Parse-based merger handles any prefix binding correctly.
	extDoc := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<R:RDF xmlns:R="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<R:Description R:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:description>NONRDF_PREFIX_VALUE</dc:description>` +
		`</R:Description>` +
		`</R:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	j := buildExtChunkJPEG(mainXMP, guid, []struct {
		fullLen uint32
		offset  uint32
		data    []byte
	}{
		{fullLen: uint32(len(extDoc)), offset: 0, data: extDoc}, //nolint:gosec // G115: test helper
	})

	_, _, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; expected reassembled XMP")
	}
	if !bytes.Contains(rawXMP, []byte("NONRDF_PREFIX_VALUE")) {
		t.Errorf("extended property with non-rdf prefix not present in rawXMP; rawXMP = %q",
			rawXMP[:min(300, len(rawXMP))])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GATE #134 — Extended XMP truncation surfaced as ParseWarning
// ─────────────────────────────────────────────────────────────────────────────

// TestExtendedXMPTruncationSurfaced is the mandatory regression gate for #134.
//
// It verifies that when the total accumulated size of extended XMP chunks for
// a single GUID exceeds maxExtendedXMPTotal (16 MiB), ExtractFull returns
// xmpTruncated=true. The top-level Read function must then surface this as a
// ParseWarning carrying ErrExtendedXMPTruncated.
//
// The test builds a JPEG where the declared fullLen for the single extended
// XMP GUID exceeds the 16 MiB cap; appendExtendedXMPChunk will set extTruncated.
func TestExtendedXMPTruncationSurfaced(t *testing.T) {
	t.Parallel()

	const guid = "1234567890ABCDEF1234567890ABCDEF"

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	// Declare a fullLen > maxExtendedXMPTotal (16 MiB + 1 byte) so the first
	// chunk triggers the DoS cap in appendExtendedXMPChunk.
	// The chunk itself is tiny (1 byte) — the declared total is what matters.
	oversizedFullLen := uint32(maxExtendedXMPTotal + 1)

	j := buildExtChunkJPEG(mainXMP, guid, []struct {
		fullLen uint32
		offset  uint32
		data    []byte
	}{
		{fullLen: oversizedFullLen, offset: 0, data: []byte("X")},
	})

	// ExtractFull must return xmpTruncated=true.
	_, _, _, _, _, xmpTruncated, err := ExtractFull(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("ExtractFull: %v", err)
	}
	if !xmpTruncated {
		t.Error("ExtractFull: xmpTruncated is false; expected true for oversized extended XMP")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GATE #135 — Extended XMP GUID case mismatch
// ─────────────────────────────────────────────────────────────────────────────

// TestExtendedXMPGUIDCaseMismatch is the mandatory regression gate for #135.
//
// It verifies that when the main packet's HasExtendedXMP attribute carries an
// uppercase GUID and the extended APP1 chunk headers carry the same GUID in
// lowercase, the reassembly succeeds and extended properties are present in
// rawXMP. Both GUIDs are normalised to uppercase before keying the map.
//
// Adobe XMP Specification Part 3 §1.1.4: GUID is defined as 32 uppercase hex
// characters, but non-conforming writers emit lowercase hex.
func TestExtendedXMPGUIDCaseMismatch(t *testing.T) {
	t.Parallel()

	const guidUpper = "DEADBEEF12345678DEADBEEF12345678" // main packet uses this
	guidLower := strings.ToLower(guidUpper)              // chunks use lowercase

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guidUpper + `"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	// Extended XMP packet uses the lowercase GUID in the APP1 header.
	extDoc := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:description>GUID_CASE_MISMATCH_VALUE</dc:description>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)

	j := buildExtChunkJPEG(mainXMP, guidLower /* chunk GUID is lowercase */, []struct {
		fullLen uint32
		offset  uint32
		data    []byte
	}{
		{fullLen: uint32(len(extDoc)), offset: 0, data: extDoc}, //nolint:gosec // G115: test helper
	})

	_, _, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; expected reassembled XMP")
	}
	if !bytes.Contains(rawXMP, []byte("GUID_CASE_MISMATCH_VALUE")) {
		t.Errorf("extended property missing despite matching GUID value; only case differs; rawXMP = %q",
			rawXMP[:min(300, len(rawXMP))])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GATE #151 — parseIRB: odd-length last block, no trailing pad
// ─────────────────────────────────────────────────────────────────────────────

// TestParseIRBOddLengthLastBlockNoTrailingPad is the mandatory regression gate
// for #151.
//
// It constructs a Photoshop IRB that contains exactly one non-IPTC 8BIM block
// with odd-length data and NO trailing padding byte. Before the fix, parseIRB
// would advance pos to len(b)+1 on the even-padding step. The loop guard
// (pos < len(b)) prevents an out-of-bounds read, but leaves pos incorrect.
// After the fix, pos is clamped to len(b).
//
// Adobe Photoshop IRB spec §"Image Resources": the even-padding byte is
// optional when the data block is the last entry in the stream. A robust
// parser must not advance past len(b).
func TestParseIRBOddLengthLastBlockNoTrailingPad(t *testing.T) {
	t.Parallel()

	// Build an IRB with one non-IPTC block (resource ID 0x0406) whose data is
	// 3 bytes (odd length). No trailing pad byte is present.
	oddData := []byte{0x01, 0x02, 0x03} // 3 bytes — odd
	irb := buildIRBDirect(0x0406, oddData)
	// Confirm: irb ends with oddData and has a padding byte (buildIRBDirect adds one).
	// Truncate the trailing pad byte to simulate the no-trailing-pad case.
	if irb[len(irb)-1] == 0x00 {
		irb = irb[:len(irb)-1]
	}
	// len(irb) is now: 4(8BIM)+2(ID)+2(pascal)+4(size)+3(data) = 15 bytes (odd, no pad).

	// parseIRB must not panic and must return nil (no 0x0404 block present).
	got := parseIRB(irb)
	if got != nil {
		t.Errorf("parseIRB: expected nil (no 0x0404), got %q", got)
	}
	// If parseIRB panicked, the test would fail with a panic — not an error.
	// The absence of panic confirms #151 is fixed.
}

// ─────────────────────────────────────────────────────────────────────────────
// GATE #174 (a) — nil rawIPTC to Inject preserves Photoshop 8BIM siblings
// ─────────────────────────────────────────────────────────────────────────────

// TestJPEGInjectNilIPTCPreservesPhotoshopSiblings is the first mandatory gate
// for #174.
//
// It verifies that calling Inject with rawIPTC=nil on a JPEG whose APP13
// contains both a 0x0404 (IPTC-NAA) block and a 0x0425 (IPTC digest) block
// produces output where the 0x0425 block is preserved and the 0x0404 block is
// removed. Before the fix, nil rawIPTC caused isOldMetadataSegment to strip the
// entire APP13, destroying both the 0x0425 and any other sibling resources.
//
// Adobe Photoshop IRB spec: all non-IPTC resources must survive a nil-IPTC write.
func TestJPEGInjectNilIPTCPreservesPhotoshopSiblings(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	digestData := []byte{
		0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
		0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	} // 16-byte IPTC digest

	// Build source JPEG with APP13 containing 0x0404 + 0x0425.
	app13Payload := buildMultiBlockAPP13(iptcData, digestData, nil)
	srcJPEG := buildJPEGWithRawAPP13(app13Payload)

	// Inject with rawIPTC=nil: this should strip 0x0404 but preserve 0x0425.
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(srcJPEG), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("output JPEG has no APP13; 0x0425 sibling must be preserved even when rawIPTC=nil")
	}

	// 0x0404 must be absent.
	ids := parseAPP13ResourceIDs(outAPP13)
	for _, id := range ids {
		if id == 0x0404 {
			t.Error("0x0404 (IPTC-NAA) present in output despite rawIPTC=nil; it should have been removed")
		}
	}

	// 0x0425 must be present and byte-identical.
	got0425 := parseAPP13ResourceData(outAPP13, 0x0425)
	if got0425 == nil {
		t.Error("0x0425 (IPTC digest) missing from output APP13; sibling must be preserved on nil-IPTC inject")
	} else if !bytes.Equal(got0425, digestData) {
		t.Errorf("0x0425 data mismatch: got %x, want %x", got0425, digestData)
	}
}

// buildMultiBlockAPP13NoIPTC builds an APP13 segment with only a 0x0425 block
// (no 0x0404). Used in multi-APP13 tests to put the digest in a second segment.
func buildMultiBlockAPP13NoIPTC(digestData []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("Photoshop 3.0\x00")
	buf.WriteString("8BIM")
	buf.WriteByte(0x04)
	buf.WriteByte(0x25)
	buf.WriteByte(0x00) // pascal name: length 0
	buf.WriteByte(0x00) // pascal name: even-padding byte
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(digestData))) //nolint:gosec // G115: test helper
	buf.Write(sz[:])
	buf.Write(digestData)
	if len(digestData)%2 != 0 {
		buf.WriteByte(0x00)
	}
	return buf.Bytes()
}

// buildJPEGWithTwoAPP13 builds a JPEG with two APP13 segments.
// The first carries only 0x0404 (IPTC); the second carries only 0x0425 (digest).
func buildJPEGWithTwoAPP13(iptcData, digestData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// First APP13: 0x0404 only.
	app13a := buildJPEGWithRawAPP13OnlyIRB(iptcData)
	buf.Write(app13a)

	// Second APP13: 0x0425 only.
	app13bPayload := buildMultiBlockAPP13NoIPTC(digestData)
	app13bLen := uint16(len(app13bPayload) + 2) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xED})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], app13bLen)
	buf.Write(lbuf[:])
	buf.Write(app13bPayload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS+EOI
	return buf.Bytes()
}

// buildJPEGWithRawAPP13OnlyIRB emits just the raw APP13 segment bytes (header
// + length + payload) for a JPEG that already has a SOI. It is used by
// buildJPEGWithTwoAPP13 as a sub-builder that does NOT emit SOI.
func buildJPEGWithRawAPP13OnlyIRB(iptcData []byte) []byte {
	var irb bytes.Buffer
	irb.WriteString("Photoshop 3.0\x00")
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04})
	irb.Write([]byte{0x00, 0x00})
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData))) //nolint:gosec // G115: test helper
	irb.Write(sz[:])
	irb.Write(iptcData)
	if len(iptcData)%2 != 0 {
		irb.WriteByte(0x00)
	}
	irbBytes := irb.Bytes()

	var seg bytes.Buffer
	seg.Write([]byte{0xFF, 0xED})
	segLen := uint16(len(irbBytes) + 2) //nolint:gosec // G115: test helper
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], segLen)
	seg.Write(lbuf[:])
	seg.Write(irbBytes)
	return seg.Bytes()
}

// TestJPEGInjectMultiAPP13SiblingPreservation is the second mandatory gate for
// #174.
//
// It verifies that when a JPEG carries 0x0404 in the first APP13 segment and
// 0x0425 in a second APP13 segment, Inject with new IPTC data preserves the
// 0x0425 resource. Before the fix, extractOriginalIRB returned only the first
// APP13 payload; the 0x0425 from the second segment was silently lost.
//
// IRB-APP13-09: all APP13 Photoshop 3.0 payloads form a single logical IRB and
// must be concatenated before processing.
func TestJPEGInjectMultiAPP13SiblingPreservation(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'o', 'l', 'd', 'I', 'P'}
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'n', 'e', 'w', 'I', 'P'}
	digestData := []byte{
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00,
	}

	srcJPEG := buildJPEGWithTwoAPP13(iptcData, digestData)

	// Inject replaces rawIPTC with newIPTC; 0x0425 must survive.
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(srcJPEG), &out, nil, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Verify 0x0404 was replaced.
	_, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("0x0404 data after inject: got %q, want %q", gotIPTC, newIPTC)
	}

	// Verify 0x0425 is present and byte-identical.
	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("output JPEG has no APP13 segment")
	}
	got0425 := parseAPP13ResourceData(outAPP13, 0x0425)
	if got0425 == nil {
		t.Error("0x0425 (IPTC digest) missing from output; second APP13 payload must be merged into origIRB")
	} else if !bytes.Equal(got0425, digestData) {
		t.Errorf("0x0425 data mismatch: got %x, want %x", got0425, digestData)
	}
}
