package gometadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"
)

// FuzzRead is an end-to-end fuzz target for the public Read entry point.
// It feeds arbitrary bytes through format detection, segment extraction, and
// all three metadata parsers (EXIF, IPTC, XMP).
//
// Invariants enforced:
//   - Arbitrary bytes / wrong magic / truncated container MUST NEVER panic.
//   - Strict mode: returns a non-nil error on corrupt parsed segment.
//   - Best-effort mode (default): NEVER panics even on corrupted payloads.
//   - On a successful Read, all convenience accessors must not panic.
//
// Run with: go test -fuzz=FuzzRead -fuzztime=60s .
func FuzzRead(f *testing.F) {
	// --- Seed: minimal valid JPEG (SOI + EOI) ---
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9})

	// --- Seed: minimal JPEG with a valid LE TIFF EXIF segment ---
	{
		tiff := fuzzMinimalTIFF()
		seed := fuzzBuildJPEGWithEXIF(tiff)
		f.Add(seed)
	}

	// --- Seed: JPEG with corrupted EXIF (wrong TIFF magic) ---
	{
		corrupt := []byte{'I', 'I', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		seed := fuzzBuildJPEGWithEXIF(corrupt)
		f.Add(seed)
	}

	// --- Seed: JPEG with a well-formed XMP segment ---
	{
		xmpPacket := []byte(
			`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
				`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
				`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
				`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
				`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">fuzz caption</rdf:li></rdf:Alt></dc:description>` +
				`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
		seed := fuzzBuildJPEGWithXMP(xmpPacket)
		f.Add(seed)
	}

	// --- Seed: JPEG with malformed XMP (depth > 100 causes ErrXMLNestingDepth) ---
	{
		var xmlBuf bytes.Buffer
		for i := range 102 {
			fmt.Fprintf(&xmlBuf, "<a%d>", i)
		}
		seed := fuzzBuildJPEGWithXMP(xmlBuf.Bytes())
		f.Add(seed)
	}

	// --- Seed: JPEG with IPTC APP13 segment ---
	{
		iptcRaw := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
		seed := fuzzBuildJPEGWithIPTC(iptcRaw)
		f.Add(seed)
	}

	// --- Seed: JPEG with junk IPTC bytes (non-0x1C start) ---
	{
		junkIPTC := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00}
		seed := fuzzBuildJPEGWithIPTC(junkIPTC)
		f.Add(seed)
	}

	// --- Seed: minimal PNG (signature + IHDR + IEND) ---
	f.Add(fuzzBuildMinimalPNG())

	// --- Seed: PNG with an EXIF eXIf chunk ---
	{
		tiff := fuzzMinimalTIFF()
		seed := fuzzBuildPNGWithEXIF(tiff)
		f.Add(seed)
	}

	// --- Seed: minimal WebP (RIFF/WEBP + VP8 stub) ---
	f.Add(fuzzBuildMinimalWebP())

	// --- Seed: arbitrary bytes (no recognisable magic) ---
	f.Add([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00})

	// --- Seed: empty input ---
	f.Add([]byte{})

	// --- Seed: SOI only (truncated JPEG) ---
	f.Add([]byte{0xFF, 0xD8})

	// --- Seed: TIFF little-endian magic (triggers TIFF-variant detection) ---
	f.Add([]byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00})

	// --- Seed: TIFF big-endian magic ---
	f.Add([]byte{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08})

	// --- BigTIFF seeds (task #54 final pass) ---

	// Seed: minimal BigTIFF LE — valid 16-byte header + zero-entry IFD0.
	// BigTIFF spec §2: magic 0x002B, offset-bytesize=8.
	{
		buf := make([]byte, 16+8+8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], 16)
		// IFD0 count=0, next-IFD=0 (already zero).
		f.Add(buf)
	}

	// Seed: minimal BigTIFF BE — same as LE seed but big-endian.
	{
		buf := make([]byte, 16+8+8)
		buf[0], buf[1] = 'M', 'M'
		binary.BigEndian.PutUint16(buf[2:], 0x002B)
		binary.BigEndian.PutUint16(buf[4:], 8)
		binary.BigEndian.PutUint16(buf[6:], 0)
		binary.BigEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with one Make entry (6-byte inline ASCII "Canon\x00").
	// Tests the BigTIFF inline path: 6 bytes ≤ 8-byte threshold → inline.
	{
		const (
			hdrSize  = 16
			cntSize  = 8
			entSize  = 20
			nextSize = 8
		)
		buf := make([]byte, hdrSize+cntSize+entSize+nextSize)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], hdrSize)
		binary.LittleEndian.PutUint64(buf[hdrSize:], 1)
		p := hdrSize + cntSize
		binary.LittleEndian.PutUint16(buf[p:], 0x010F) // Make tag
		binary.LittleEndian.PutUint16(buf[p+2:], 2)    // TypeASCII
		binary.LittleEndian.PutUint64(buf[p+4:], 6)    // count=6
		copy(buf[p+12:], "Canon\x00")                  // inline (6 ≤ 8)
		f.Add(buf)
	}

	// Seed: BigTIFF with bad offset-bytesize (must reject cleanly, no panic).
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 4) // invalid bytesize
		binary.LittleEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed: BigTIFF with IFD0 offset pointing far past EOF (OOB robustness).
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint64(buf[8:], 0xFFFFFFFFFFFFFF00)
		f.Add(buf)
	}

	// Seed: BigTIFF with huge IFD0 entry count (DoS guard — must clamp).
	{
		buf := make([]byte, 32)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint64(buf[8:], 16)
		binary.LittleEndian.PutUint64(buf[16:], ^uint64(0)) // MaxUint64
		f.Add(buf)
	}

	// Seed: BigTIFF with a cyclic IFD chain (cycle detection must terminate).
	{
		buf := make([]byte, 16+8+8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint64(buf[8:], 16)
		binary.LittleEndian.PutUint64(buf[16:], 0)  // count=0
		binary.LittleEndian.PutUint64(buf[24:], 16) // next-IFD = self (cycle)
		f.Add(buf)
	}

	// --- Seed: truncated JPEG APP1 with valid length but no body ---
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x0A})

	// --- Seed: JPEG with all three segments (EXIF + IPTC + XMP) ---
	{
		seed := fuzzBuildJPEGAllSegments()
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)

		// --- Best-effort mode (default): must NEVER panic ---
		m, _ := Read(r)
		if m != nil {
			// All convenience accessors must be nil-safe.
			fuzzExerciseAccessors(m)
		}

		// --- Strict mode: must NEVER panic (may return an error) ---
		_, _ = r.Seek(0, 0) // reset for second parse
		_, _ = Read(r, Strict())
	})
}

// fuzzExerciseAccessors calls every public Metadata accessor to verify
// nil-safety on a parsed result. The calls must not panic regardless of
// which segments were present or absent.
func fuzzExerciseAccessors(m *Metadata) {
	_ = m.Format()
	_ = m.RawEXIF()
	_ = m.RawIPTC()
	_ = m.RawXMP()
	_ = m.CameraModel()
	_, _, _ = m.GPS()
	_ = m.Copyright()
	_ = m.Caption()
	_, _ = m.DateTimeOriginal()
	_, _, _ = m.ExposureTime()
	_, _ = m.FNumber()
	_, _ = m.ISO()
	_, _ = m.FocalLength()
	_ = m.LensModel()
	_, _ = m.Orientation()
	_, _, _ = m.ImageSize()
	_ = m.Keywords()
	_ = m.Make()
	_ = m.Software()
	_, _ = m.DateTime()
	_, _ = m.WhiteBalance()
	_, _ = m.Flash()
	_, _ = m.ExposureMode()
	_, _ = m.Altitude()
	_, _ = m.SubjectDistance()
	_, _ = m.DigitalZoomRatio()
	_, _ = m.SceneType()
	_, _ = m.ColorSpace()
	_, _ = m.MeteringMode()
	_ = m.Creator()
}

// ---------------------------------------------------------------------------
// Seed-corpus builder helpers — minimal but structurally valid containers.
// ---------------------------------------------------------------------------

// fuzzMinimalTIFF builds a tiny valid TIFF payload (LE, 1 IFD0 entry) that
// exif.Parse will accept.
func fuzzMinimalTIFF() []byte {
	order := binary.LittleEndian
	// header(8) + ifd_count(2) + 1 entry(12) + next_ifd(4)
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8
	order.PutUint16(buf[8:], 1) // 1 entry
	// Tag 0x010E ImageDescription, TypeASCII, count=3, inline "ok\x00"
	order.PutUint16(buf[10:], 0x010E)
	order.PutUint16(buf[12:], 2) // TypeASCII
	order.PutUint32(buf[14:], 3) // count
	copy(buf[18:], "ok\x00")     // inline value (≤4 bytes)
	order.PutUint32(buf[22:], 0) // next IFD = 0
	return buf
}

// fuzzBuildJPEGWithEXIF constructs a minimal JPEG containing an APP1 EXIF
// segment with the given TIFF payload.
func fuzzBuildJPEGWithEXIF(tiffData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	payload := append([]byte("Exif\x00\x00"), tiffData...)
	length := uint16(len(payload) + 2) //nolint:gosec // G115: seed builder, controlled data
	buf.Write([]byte{0xFF, 0xE1})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(payload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// fuzzBuildJPEGWithXMP constructs a minimal JPEG containing an APP1 XMP
// segment with the given XMP packet bytes.
func fuzzBuildJPEGWithXMP(xmpPacket []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	ns := "http://ns.adobe.com/xap/1.0/\x00"
	payload := append([]byte(ns), xmpPacket...)
	length := uint16(len(payload) + 2) //nolint:gosec // G115: seed builder, controlled data
	buf.Write([]byte{0xFF, 0xE1})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(payload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// fuzzBuildJPEGWithIPTC constructs a minimal JPEG containing an APP13 segment
// wrapping the given raw IPTC bytes inside a Photoshop IRB.
func fuzzBuildJPEGWithIPTC(iptcRaw []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	var irb bytes.Buffer
	irb.WriteString("Photoshop 3.0\x00")
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04}) // IPTC-NAA resource 0x0404
	irb.Write([]byte{0x00, 0x00}) // empty Pascal string
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcRaw))) //nolint:gosec // G115: seed builder, controlled data
	irb.Write(sz[:])
	irb.Write(iptcRaw)
	if len(iptcRaw)%2 != 0 {
		irb.WriteByte(0x00) // Photoshop IRB alignment pad
	}
	length := uint16(irb.Len() + 2) //nolint:gosec // G115: seed builder, controlled data
	buf.Write([]byte{0xFF, 0xED})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(irb.Bytes())

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// fuzzBuildMinimalPNG builds a minimal PNG (signature + IHDR + IEND) with
// correct CRC values so the PNG extractor recognises the container.
func fuzzBuildMinimalPNG() []byte {
	var buf bytes.Buffer
	// PNG signature (PNG §5.2).
	buf.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})

	writePNG := func(chunkType string, data []byte) {
		var lbuf [4]byte
		binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: seed builder, controlled data
		buf.Write(lbuf[:])
		buf.WriteString(chunkType)
		buf.Write(data)
		h := crc32.NewIEEE()
		_, _ = h.Write([]byte(chunkType))
		_, _ = h.Write(data)
		binary.BigEndian.PutUint32(lbuf[:], h.Sum32())
		buf.Write(lbuf[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height
	ihdr[8] = 8                             // bit depth
	ihdr[9] = 2                             // colour type: RGB
	writePNG("IHDR", ihdr)
	writePNG("IEND", nil)
	return buf.Bytes()
}

// fuzzBuildPNGWithEXIF builds a minimal PNG containing an eXIf chunk.
func fuzzBuildPNGWithEXIF(tiffData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})

	writeChunk := func(chunkType string, data []byte) {
		var lbuf [4]byte
		binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: seed builder, controlled data
		buf.Write(lbuf[:])
		buf.WriteString(chunkType)
		buf.Write(data)
		h := crc32.NewIEEE()
		_, _ = h.Write([]byte(chunkType))
		_, _ = h.Write(data)
		binary.BigEndian.PutUint32(lbuf[:], h.Sum32())
		buf.Write(lbuf[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width=1
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height=1
	ihdr[8] = 8
	ihdr[9] = 2
	writeChunk("IHDR", ihdr)
	writeChunk("eXIf", tiffData)
	writeChunk("IEND", nil)
	return buf.Bytes()
}

// fuzzBuildMinimalWebP builds a minimal RIFF/WebP stream (RIFF header + WEBP
// FourCC + VP8 chunk with a stub bitstream) so the WebP extractor recognises
// the container.
func fuzzBuildMinimalWebP() []byte {
	var body bytes.Buffer

	// Minimal VP8 lossy bitstream stub (10 bytes) sufficient for magic detection.
	vp8stub := []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}
	body.WriteString("VP8 ")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(vp8stub))) //nolint:gosec // G115: seed builder, controlled data
	body.Write(sz[:])
	body.Write(vp8stub)

	// RIFF envelope.
	totalBodySize := uint32(4 + body.Len()) //nolint:gosec // G115: seed builder, controlled data
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.LittleEndian.PutUint32(sz[:], totalBodySize)
	out.Write(sz[:])
	out.WriteString("WEBP")
	out.Write(body.Bytes())
	return out.Bytes()
}

// fuzzBuildJPEGAllSegments constructs a JPEG containing all three metadata
// segments (EXIF APP1, IPTC APP13, XMP APP1) so the end-to-end dispatcher is
// exercised in a single input.
func fuzzBuildJPEGAllSegments() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	writeAPP := func(marker byte, payload []byte) {
		length := uint16(len(payload) + 2) //nolint:gosec // G115: seed builder, controlled data
		buf.Write([]byte{0xFF, marker})
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], length)
		buf.Write(lb[:])
		buf.Write(payload)
	}

	// APP1: EXIF
	tiff := fuzzMinimalTIFF()
	exifPayload := append([]byte("Exif\x00\x00"), tiff...)
	writeAPP(0xE1, exifPayload)

	// APP13: IPTC
	iptcRaw := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'k', 'e', 'y'}
	var irb bytes.Buffer
	irb.WriteString("Photoshop 3.0\x00")
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04, 0x00, 0x00})
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcRaw))) //nolint:gosec // G115: seed builder, controlled data
	irb.Write(sz[:])
	irb.Write(iptcRaw)
	writeAPP(0xED, irb.Bytes())

	// APP1: XMP
	xmpPacket := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`
	xmpPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte(xmpPacket)...)
	writeAPP(0xE1, xmpPayload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}
