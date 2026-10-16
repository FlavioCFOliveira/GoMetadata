package exif

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// FuzzParseEXIF exercises the EXIF parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...
func FuzzParseEXIF(f *testing.F) {
	// Security audit 2026-10-16: F-06a (IFDs of a chain addressing one
	// thumbnail range) and F-06b (IFD0 entries addressing private IFDs whose
	// data cannot all be carried), scaled down.
	f.Add(auditThumbChain(40, 4096, func(int) int { return 0 }, 4096))
	f.Add(auditPrivateIFDs(8, func(int) (int, int) { return 0, 4000 }, 4096))
	f.Add(auditPrivateIFDs(3, func(i int) (int, int) { return i * 100, 100 }, 400))
	// Seed corpus: minimal valid little-endian TIFF header.
	f.Add([]byte("II\x2A\x00\x08\x00\x00\x00"))
	// Seed corpus: minimal valid big-endian TIFF header.
	f.Add([]byte("MM\x00\x2A\x00\x00\x00\x08"))

	// Seed corpus: zero-entry IFD, little-endian.
	// header(8) + ifd_count(2) + next_ifd(4) — IFD0 has 0 entries.
	{
		buf := make([]byte, 8+2+4)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)  // IFD0 at offset 8
		order.PutUint16(buf[8:], 0)  // 0 entries
		order.PutUint32(buf[10:], 0) // next IFD = 0
		f.Add(buf)
	}

	// Seed corpus: zero-entry IFD, big-endian.
	{
		buf := make([]byte, 8+2+4)
		order := binary.BigEndian
		buf[0], buf[1] = 'M', 'M'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)
		order.PutUint16(buf[8:], 0)
		order.PutUint32(buf[10:], 0)
		f.Add(buf)
	}

	// Seed corpus: single IFD entry whose value fits inline (≤4 bytes).
	// Tag 0x0100 (ImageWidth), TypeSHORT (3), count=1, value=800 LE inline.
	{
		buf := make([]byte, 8+2+12+4)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)
		order.PutUint16(buf[8:], 1) // 1 entry
		p := buf[10:]
		order.PutUint16(p[0:], 0x0100)  // ImageWidth
		order.PutUint16(p[2:], 3)       // TypeSHORT
		order.PutUint32(p[4:], 1)       // count = 1
		order.PutUint16(p[8:], 800)     // inline value
		order.PutUint32(buf[10+12:], 0) // next IFD = 0
		f.Add(buf)
	}

	// --- BigTIFF seeds (task #54 final pass) ---

	// Seed: minimal BigTIFF LE header only (no IFD entries).
	// BigTIFF spec §2: magic 0x002B, offset-bytesize=8.
	{
		buf := make([]byte, 16+8+8) // header + count(8) + next(8)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
		order.PutUint16(buf[4:], 8)      // offset-bytesize=8
		order.PutUint16(buf[6:], 0)      // constant 0
		order.PutUint64(buf[8:], 16)     // IFD0 at offset 16
		order.PutUint64(buf[16:], 0)     // count = 0
		// next-IFD = 0 (already zero)
		f.Add(buf)
	}

	// Seed: minimal BigTIFF BE header only.
	{
		buf := make([]byte, 16+8+8)
		order := binary.BigEndian
		buf[0], buf[1] = 'M', 'M'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], 16)
		order.PutUint64(buf[16:], 0)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with one Make entry (6-byte ASCII "Canon\x00", inline).
	// BigTIFF inline threshold = 8, so 6-byte ASCII is stored inline.
	{
		const (
			hdrSize  = 16
			cntSize  = 8
			entSize  = 20
			nextSize = 8
		)
		buf := make([]byte, hdrSize+cntSize+entSize+nextSize)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], hdrSize)
		order.PutUint64(buf[hdrSize:], 1) // 1 entry
		p := hdrSize + cntSize
		order.PutUint16(buf[p:], 0x010F) // Make
		order.PutUint16(buf[p+2:], 2)    // TypeASCII
		order.PutUint64(buf[p+4:], 6)    // count = 6 ("Canon\x00")
		copy(buf[p+12:], "Canon\x00")    // inline value (6 bytes ≤ 8)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with bad offset-bytesize (should be rejected cleanly).
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 4) // invalid: not 8
		binary.LittleEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with IFD0 offset pointing beyond EOF.
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint64(buf[8:], 0xFFFFFFFFFFFFFF00) // way past EOF
		f.Add(buf)
	}

	// Seed: BigTIFF LE with one entry using LONG8 type (type code 16).
	// StripOffsets as LONG8, count=1, value=0x10 (inline, 8 bytes).
	{
		const (
			hdrSize  = 16
			cntSize  = 8
			entSize  = 20
			nextSize = 8
		)
		buf := make([]byte, hdrSize+cntSize+entSize+nextSize)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], hdrSize)
		order.PutUint64(buf[hdrSize:], 1)
		p := hdrSize + cntSize
		order.PutUint16(buf[p:], 0x0111)  // StripOffsets
		order.PutUint16(buf[p+2:], 16)    // LONG8
		order.PutUint64(buf[p+4:], 1)     // count=1
		order.PutUint64(buf[p+12:], 0x10) // inline value=16
		f.Add(buf)
	}

	// Seed: the committed real-world BigTIFF fixture (produced by
	// `tiffcp -8`), added directly as fuzz-corpus material (task #264). This
	// gives the mutator a structurally rich, spec-conformant BigTIFF starting
	// point — 22 IFD0 entries spanning inline SHORT/RATIONAL/LONG8 values and
	// a large out-of-line UNDEFINED (ICC profile) blob — rather than only the
	// minimal synthetic seeds above.
	if data, err := os.ReadFile("testdata/BigTIFF_LE.tif"); err == nil {
		f.Add(data)
	}

	// Seeds with a MakerNote (tasks #317, #318): the TIFF-relative layouts, the
	// Nikon PreviewIFD that runs past the declared end of the note, and a
	// value stored outside the note (testutil.MakerNoteTIFF).
	for _, spec := range []testutil.MNSpec{
		{Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true, Footer: true, PadBefore: 6, Entries: mnEntries()},
		{Make: "OLYMPUS OPTICAL CO.,LTD", Order: binary.LittleEndian, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: 6,
			Entries: append(mnEntries(), testutil.MNEntry{Tag: 0x0004, Type: 4, Count: 3, Value: mnTriple(), Outside: true})},
		nikon3Spec([]byte{0xFF, 0xD8, 1, 2, 3, 4, 0xFF, 0xD9}),
	} {
		tiff, _ := testutil.MakerNoteTIFF(spec)
		f.Add(tiff)
	}
	// Seeds (task #362): a Kodak private IFD behind IFD0 tag 0x8290 and
	// Interoperability pointers that loop to IFD0 and to the Exif IFD; Olympus
	// notes with a CameraSettings preview offset; headerless Nikon notes of
	// both byte orders (task #364).
	for _, interop := range []func(ifd0, exifIFD uint32) uint32{noInterop, func(ifd0, _ uint32) uint32 { return ifd0 }, func(_, exifIFD uint32) uint32 { return exifIFD }} {
		kodak, _ := kodakStream(interop, 2)
		f.Add(kodak)
	}
	f.Add(olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", []testutil.MNEntry{
		{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)}, {Tag: 0x0102, Type: 4, Count: 1, Value: long(321)}}))
	f.Add(olympusStream("OM Digital Solutions", "OM SYSTEM\x00\x00\x00II", []testutil.MNEntry{
		{Tag: 0x0101, Type: 4, Count: 1, Value: long(1)}, {Tag: 0x0102, Type: 4, Count: 1, Value: long(2)}}))
	for _, order := range []testOrder{binary.LittleEndian, binary.BigEndian} {
		tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{Make: "NIKON", Order: order, TIFFRelative: true, PadBefore: 6, Entries: mnEntries()})
		f.Add(tiff)
	}
	// Seed: an IFD1 whose pointer tags address IFDs of their own (task #319).
	{
		e := chainEXIF(1, 1, &SubIFDs{
			Exif: &IFD{Entries: []IFDEntry{chainEnt(TagExposureTime, TypeRational, 1, []byte{1, 0, 0, 0, 100, 0, 0, 0})}},
			GPS:  &IFD{Entries: []IFDEntry{chainEnt(TagGPSVersionID, TypeByte, 4, []byte{2, 4, 0, 0})}},
		}, false)
		if out, err := Encode(e); err == nil {
			f.Add(out)
		}
	}

	// Seeds (task #368, #370): the strips of an uncompressed thumbnail, private
	// IFD0 pointers, and the other forms of the Olympus preview offset.
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		strips, _ := rawStripStream(order, TypeLong, TypeLong, []int{50, 51, 0, 7}, []int{0, 3, 1}, false)
		f.Add(strips)
	}
	shortStrips, _ := rawStripStream(binary.LittleEndian, TypeShort, TypeShort, []int{33, 34}, []int{0}, true)
	f.Add(shortStrips)
	privStream, _ := privateStream(ptrSpec{0x014A, 4, 3}, ptrSpec{0x0190, 4, 1}, ptrSpec{0xC000, 13, 1})
	f.Add(privStream)
	f.Add(olympStream([]testutil.MNEntry{{Tag: 0x2020, Type: 7, Count: 42, Value: previewIFDValue(5000, 321)}}))
	f.Add(olympStream([]testutil.MNEntry{{Tag: 0x1036, Type: 4, Count: 1, Value: long(5000)}, {Tag: 0x1037, Type: 4, Count: 1, Value: long(321)}}))

	f.Fuzz(func(t *testing.T, b []byte) {
		// Must not panic on any input.
		e, err := Parse(b)
		if err != nil {
			// Parse errors are expected for arbitrary inputs; return cleanly.
			return
		}

		// Post-success validity assertions: a successfully parsed EXIF must have
		// a recognised byte order (TIFF §2).
		if e != nil {
			if e.ByteOrder != binary.LittleEndian && e.ByteOrder != binary.BigEndian {
				t.Errorf("ByteOrder is neither LE nor BE after successful parse")
			}

			// IFD0 must be non-nil on a successful parse (TIFF §2 requires at least
			// one IFD to be present, but we tolerate zero-entry IFDs defensively).
			if e.IFD0 != nil {
				// Calling Get must not panic regardless of tag.
				_ = e.IFD0.Get(TagMake)
			}

			// task #264: fuzz the Encode round-trip too, not just Parse. Any
			// successfully parsed EXIF (classic or BigTIFF) must Encode without
			// panicking, and a successful Encode must produce output that
			// re-Parses without panicking and never silently changes container
			// provenance (BigTIFF must stay BigTIFF — audit finding #107).
			// Deep per-tag equality is intentionally NOT asserted here: that is
			// covered precisely by the deterministic round-trip tests in
			// bigtiff_write_test.go and TestConformance_R14_bigtiff_roundtrip_fidelity;
			// this fuzz target's job is to catch crashes and format-downgrade
			// regressions across the full space of malformed/edge-case inputs.
			encoded, encErr := Encode(e)
			if encErr == nil {
				e2, reErr := Parse(encoded)
				if reErr == nil && e2 != nil && e.BigTIFF != e2.BigTIFF {
					t.Errorf("Encode round-trip changed BigTIFF provenance: before=%v after=%v", e.BigTIFF, e2.BigTIFF)
				}
				// EncodedSize replays Encode's layout, including the IFDs
				// attached to the chain (task #319).
				if n, sizeErr := EncodedSize(e); sizeErr != nil || n != len(encoded) {
					t.Errorf("EncodedSize = %d, %v; len(Encode) = %d", n, sizeErr, len(encoded))
				}
			}
			// Tasks #317, #318: the MakerNote repair must not panic, must not
			// modify its inputs, and its output must parse.
			before := append([]byte(nil), b...)
			rebased, rbErr := EncodeRebased(e, b)
			if rbErr == nil {
				if _, reErr := Parse(rebased); reErr != nil && encErr == nil {
					t.Errorf("EncodeRebased output does not parse although Encode's does: %v", reErr)
				}
			}
			if !bytes.Equal(before, b) {
				t.Error("EncodeRebased modified the source stream")
			}
			// Task #362: the data of the MakerNote that lies outside the EXIF
			// stream is found without panicking, on the stream and on what the
			// writer made of it, and leaves the stream as it was.
			for _, o := range ExternalOffsets(b) {
				// Task #370: the field is inside the MakerNote, inside the stream.
				if o.Field < o.MNStart || o.Field+4 > o.MNStart+o.MNLen || o.MNStart+o.MNLen > len(b) {
					t.Errorf("ExternalOffsets: field %d outside the MakerNote [%d,+%d) of the %d-byte stream", o.Field, o.MNStart, o.MNLen, len(b))
				}
			}
			if rbErr == nil {
				_ = ExternalOffsets(rebased)
			}
			if !bytes.Equal(before, b) {
				t.Error("ExternalOffsets modified its argument")
			}
			fuzzThumbnailStrips(t, b)
		}
	})
}

// fuzzThumbnailStrips checks the strips of the thumbnail (task #368): Parse with
// CarryThumbnailStrips never panics; when it carries strips, Encode writes
// them, EncodedSize agrees with the length, and a second parse of the output
// reads the same strip bytes.
func fuzzThumbnailStrips(t *testing.T, b []byte) {
	t.Helper()
	e, err := Parse(b, CarryThumbnailStrips())
	if err != nil || e.IFD0 == nil {
		return
	}
	var want [][]byte
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		for _, s := range ifd.thumbnailStrips() {
			want = append(want, bytes.Clone(s))
		}
	}
	out, err := Encode(e)
	if err != nil {
		return
	}
	if n, sizeErr := EncodedSize(e); sizeErr != nil || n != len(out) {
		t.Errorf("EncodedSize = %d, %v; len(Encode) = %d (with strips)", n, sizeErr, len(out))
	}
	if len(want) == 0 {
		return
	}
	e2, err := Parse(out, CarryThumbnailStrips())
	if err != nil {
		t.Fatalf("Parse of an output with strips: %v", err)
	}
	var got [][]byte
	for ifd := e2.IFD0.Next; ifd != nil; ifd = ifd.Next {
		got = append(got, ifd.thumbnailStrips()...)
	}
	if !equalStrips(got, want) {
		t.Errorf("the strips differ after Encode: %d before, %d after", len(want), len(got))
	}
}
