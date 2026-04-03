package tiff

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
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

	var entries []entry

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
	dataOff := uint32(headerSize + ifdSize)

	var valueBuf bytes.Buffer
	for _, s := range specs {
		typ := uint16(7)  // UNDEFINED
		cnt := uint32(len(s.payload))
		off := dataOff + uint32(valueBuf.Len())
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
	order.PutUint16(buf[ifdStart:], uint16(len(entries)))
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
