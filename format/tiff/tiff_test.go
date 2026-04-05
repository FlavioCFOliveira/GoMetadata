package tiff

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// buildMinimalTIFF creates a TIFF file with optional IPTC (tag 0x83BB) and
// XMP (tag 0x02BC) tags stored as external offset/length values.
func buildMinimalTIFF(order binary.ByteOrder, iptc, xmp []byte) []byte {
	type entry struct {
		tag  uint16
		typ  uint16
		cnt  uint32
		val  uint32 // offset or inline
		data []byte
	}

	// Calculate where extra data starts: header(8) + ifd_count(2) + entries*12 + next_ifd(4)
	// We'll figure out exact positions below.

	type entrySpec struct {
		tag     uint16
		payload []byte
	}
	var specs []entrySpec
	if iptc != nil {
		specs = append(specs, entrySpec{0x83BB, iptc})
	}
	if xmp != nil {
		specs = append(specs, entrySpec{0x02BC, xmp})
	}

	// Header = 8, ifd = 2 + len(specs)*12 + 4
	headerSize := 8
	ifdSize := 2 + len(specs)*12 + 4
	dataOff := uint32(headerSize + ifdSize) //nolint:gosec // G115: test helper, intentional type cast

	entries := make([]entry, 0, len(specs))
	var valueBuf bytes.Buffer
	for _, s := range specs {
		typ := uint16(7)                        // UNDEFINED
		cnt := uint32(len(s.payload))           //nolint:gosec // G115: test helper, intentional type cast
		off := dataOff + uint32(valueBuf.Len()) //nolint:gosec // G115: test helper, intentional type cast
		entries = append(entries, entry{s.tag, typ, cnt, off, s.payload})
		valueBuf.Write(s.payload)
	}

	buf := make([]byte, int(dataOff)+valueBuf.Len())

	// Byte order.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 offset

	// IFD.
	ifdStart := 8
	order.PutUint16(buf[ifdStart:], uint16(len(entries))) //nolint:gosec // G115: test helper, intentional type cast
	for i, e := range entries {
		p := ifdStart + 2 + i*12
		order.PutUint16(buf[p:], e.tag)
		order.PutUint16(buf[p+2:], e.typ)
		order.PutUint32(buf[p+4:], e.cnt)
		order.PutUint32(buf[p+8:], e.val)
	}
	// next-IFD = 0
	copy(buf[ifdStart+2+len(entries)*12:], []byte{0, 0, 0, 0})

	// Copy value area.
	copy(buf[dataOff:], valueBuf.Bytes())
	return buf
}

func TestExtractBasic(t *testing.T) {
	wantIPTC := []byte("iptc-test-data-long-enough-for-external-storage")
	wantXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalTIFF(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

func TestExtractNoMetadata(t *testing.T) {
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC != nil {
		t.Error("expected nil rawIPTC")
	}
	if rawXMP != nil {
		t.Error("expected nil rawXMP")
	}
}

func TestExtractOverflow(t *testing.T) {
	// Craft a TIFF with cnt = MaxUint32 for an IPTC tag — should not panic.
	var buf bytes.Buffer
	order := binary.LittleEndian
	var header [8]byte
	header[0], header[1] = 'I', 'I'
	order.PutUint16(header[2:], 0x002A)
	order.PutUint32(header[4:], 8)
	buf.Write(header[:])

	// IFD: 1 entry
	order.PutUint16(header[:], 1)
	buf.Write(header[:2])

	// Entry: tag=0x83BB, type=7 (UNDEFINED), cnt=MaxUint32, offset=0xFFFFFFFF
	var e [12]byte
	order.PutUint16(e[0:], 0x83BB)
	order.PutUint16(e[2:], 7)
	order.PutUint32(e[4:], math.MaxUint32)
	order.PutUint32(e[8:], 0xFFFFFFFF)
	buf.Write(e[:])
	// next-IFD
	buf.Write([]byte{0, 0, 0, 0})

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract with overflow tag should not error: %v", err)
	}
	// The oversized entry should be skipped.
	if rawIPTC != nil {
		t.Error("expected rawIPTC to be nil (overflow entry skipped)")
	}
}

func TestInjectIPTCRoundTrip(t *testing.T) {
	wantIPTC := []byte("original-iptc-payload-that-is-long-enough")
	wantXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalTIFF(binary.LittleEndian, wantIPTC, wantXMP)

	newIPTC := []byte("updated-iptc-data")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, newIPTC, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC after inject: got %q, want %q", gotIPTC, newIPTC)
	}
	// XMP should be unchanged (we passed nil for rawXMP in Inject).
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("XMP should be unchanged: got %q, want %q", gotXMP, wantXMP)
	}
}

func TestInjectXMPRoundTrip(t *testing.T) {
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	newXMP := []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, newXMP); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject: %v", err)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP after inject: got %q, want %q", gotXMP, newXMP)
	}
}

func TestInjectPassThrough(t *testing.T) {
	// When rawIPTC and rawXMP are both nil, the output should equal the input.
	data := buildMinimalTIFF(binary.LittleEndian, []byte("x"), nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Error("pass-through inject should not modify the data")
	}
}

func BenchmarkTIFFExtract(b *testing.B) {
	iptc := []byte("some-iptc-payload-data-for-benchmarking")
	xmp := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalTIFF(binary.LittleEndian, iptc, xmp)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}

// buildTIFFWithPrivateTag creates a minimal little-endian TIFF whose IFD0
// contains a single entry with the given tag, TIFF type code, and value bytes.
// When len(value) > 4 the value is placed in the out-of-line data area and the
// entry's value field holds the offset; otherwise the value is stored inline.
//
// Layout:
//
//	[0..7]   TIFF header (LE, magic 0x002A, IFD0 offset = 8)
//	[8..9]   entry count = 1
//	[10..21] single IFD entry (12 bytes)
//	[22..25] next-IFD pointer = 0
//	[26..]   out-of-line value data (when len(value) > 4)
func buildTIFFWithPrivateTag(tag uint16, typ uint16, value []byte) []byte {
	const ifd0Off = 8
	const dataOff = uint32(26) // 8 (hdr) + 2 (count) + 12 (entry) + 4 (next-IFD)

	order := binary.LittleEndian
	total := len(value)

	// Buffer size: fixed layout + out-of-line value (if any).
	bufLen := int(dataOff)
	if total > 4 {
		bufLen += total
	}
	buf := make([]byte, bufLen)

	// Header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD: entry count.
	order.PutUint16(buf[ifd0Off:], 1)

	// Entry.
	e := ifd0Off + 2
	order.PutUint16(buf[e:], tag)
	order.PutUint16(buf[e+2:], typ)
	order.PutUint32(buf[e+4:], uint32(total)) //nolint:gosec // G115: test helper, intentional type cast
	if total <= 4 {
		copy(buf[e+8:], value)
	} else {
		order.PutUint32(buf[e+8:], dataOff)
		copy(buf[dataOff:], value)
	}

	// Next-IFD pointer = 0.
	order.PutUint32(buf[e+12:], 0)
	return buf
}

// TestPrivateShortTagRoundTrip verifies that a private SHORT tag (inline, 2 bytes,
// known type) survives a full Extract → Inject (with IPTC update) → Extract cycle.
// This exercises the path where IFD0 is re-encoded by exif.Encode.
func TestPrivateShortTagRoundTrip(t *testing.T) {
	const privateTag = uint16(0x4321) // private-use tag, not in EXIF registry
	const shortType = uint16(3)       // TypeShort

	// Encode value 0xABCD as little-endian SHORT.
	value := []byte{0xCD, 0xAB}
	data := buildTIFFWithPrivateTag(privateTag, shortType, value)

	// Sanity: original data must parse cleanly.
	if _, err := exif.Parse(data); err != nil {
		t.Fatalf("exif.Parse on original data: %v", err)
	}

	// Inject a new IPTC payload — this forces re-encoding of the whole TIFF.
	newIPTC := []byte("iptc-payload-for-private-tag-test-long-enough")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, newIPTC, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Re-extract and verify the private tag is still present.
	rawEXIF, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC after round-trip: got %q, want %q", gotIPTC, newIPTC)
	}

	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("exif.Parse after round-trip: %v", err)
	}

	entry := parsed.IFD0.Get(exif.TagID(privateTag))
	if entry == nil {
		t.Fatalf("private tag 0x%04X not found in IFD0 after round-trip", privateTag)
	}
	if entry.Type != exif.TypeShort {
		t.Errorf("private tag type = %d, want %d (TypeShort)", entry.Type, exif.TypeShort)
	}
	if len(entry.Value) < 2 {
		t.Fatalf("private tag value too short: %d bytes", len(entry.Value))
	}
	got := binary.LittleEndian.Uint16(entry.Value)
	if got != 0xABCD {
		t.Errorf("private tag value = 0x%04X, want 0xABCD", got)
	}
}

// TestPrivateUndefinedTagRoundTrip verifies that a private UNDEFINED tag whose
// value exceeds 4 bytes (out-of-line storage) is faithfully preserved through
// a full Extract → Inject (with XMP update) → Extract cycle.
// UNDEFINED (type 7) has a defined byte size of 1, so exif.Encode can locate
// and copy the value area correctly.
func TestPrivateUndefinedTagRoundTrip(t *testing.T) {
	const privateTag = uint16(0x5678)
	const undefinedType = uint16(7) // TypeUndefined, size = 1 byte per unit

	// 12-byte value — larger than 4, so stored out-of-line.
	value := []byte("private-data")
	data := buildTIFFWithPrivateTag(privateTag, undefinedType, value)

	if _, err := exif.Parse(data); err != nil {
		t.Fatalf("exif.Parse on original data: %v", err)
	}

	newXMP := []byte("<?xpacket begin='' id='x'?><xmpmeta/><?xpacket end='r'?>")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, newXMP); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP after round-trip: got %q, want %q", gotXMP, newXMP)
	}

	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("exif.Parse after round-trip: %v", err)
	}

	entry := parsed.IFD0.Get(exif.TagID(privateTag))
	if entry == nil {
		t.Fatalf("private tag 0x%04X not found in IFD0 after round-trip", privateTag)
	}
	if !bytes.Equal(entry.Value, value) {
		t.Errorf("private tag value = %q, want %q", entry.Value, value)
	}
}

// TestUnknownTypeTagRoundTrip verifies the documented behaviour for entries
// with unknown TIFF type codes (not defined in TIFF 6.0 §2): the 4-byte IFD
// field is preserved verbatim. If that field was an offset to out-of-line data,
// the data is not copied — this is an explicitly documented design constraint
// (see exif.Encode documentation).
func TestUnknownTypeTagRoundTrip(t *testing.T) {
	const privateTag = uint16(0x9ABC)
	const unknownType = uint16(0xFF) // not a valid TIFF type; typeSize returns 0

	// For an unknown type, exif parser stores the raw 4-byte IFD value field.
	// We put a recognisable sentinel in that field.
	sentinel := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	data := buildTIFFWithPrivateTag(privateTag, unknownType, sentinel)

	parsed, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	// Verify the entry is present before round-trip.
	before := parsed.IFD0.Get(exif.TagID(privateTag))
	if before == nil {
		t.Fatalf("private tag 0x%04X not found before round-trip", privateTag)
	}
	if !bytes.Equal(before.Value, sentinel) {
		t.Errorf("value before round-trip = %x, want %x", before.Value, sentinel)
	}

	// Force re-encoding via Inject with an IPTC update.
	newIPTC := []byte("iptc-for-unknown-type-tag-test-data-that-is-long-enough")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, newIPTC, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	after, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("exif.Parse after round-trip: %v", err)
	}

	entry := after.IFD0.Get(exif.TagID(privateTag))
	if entry == nil {
		t.Fatalf("private tag 0x%04X not found after round-trip", privateTag)
	}
	// The 4-byte field must be preserved verbatim (documented behaviour).
	if !bytes.Equal(entry.Value, sentinel) {
		t.Errorf("4-byte IFD field after round-trip = %x, want %x", entry.Value, sentinel)
	}
}
