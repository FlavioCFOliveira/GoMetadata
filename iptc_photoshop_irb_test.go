package gometadata

// iptc_photoshop_irb_test.go — end-to-end tests for the Photoshop image
// resource block (IRB) shared parser:
//   - task #301: TIFF files that store IPTC only in the IRB of tag 0x8649;
//   - task #284: the IPTC digest (IRB resource 0x0425) after an IPTC write,
//     for JPEG (APP13) and TIFF (tag 0x8649).

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest algorithm, not used for security
	"encoding/binary"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// psirOnlyTIFF is a 1x1 TIFF whose only IPTC is the 0x0404 resource of the
// IRB in tag 0x8649, followed by a 0x0425 digest of that IIM. The IRB bytes
// were written by ExifTool 13.55 ("exiftool -IPTC:...=" on a PNG, decoded
// from its "Raw profile type iptc" chunk); the TIFF wrapper was built around
// them. "exiftool -a -G1 -IPTC:all" on the file reports the datasets listed
// in TestRead_TIFFIPTCInPhotoshopIRB.
const psirOnlyTIFF = "testdata/fixtures/tiff_iptc_in_psir_only.tif"

// TestRead_TIFFIPTCInPhotoshopIRB (task #301): Read returns exactly the IPTC
// datasets that ExifTool reports for a TIFF holding IPTC only in tag 0x8649.
func TestRead_TIFFIPTCInPhotoshopIRB(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(psirOnlyTIFF)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.IPTC == nil {
		t.Fatal("IPTC = nil; the IIM in tag 0x8649 resource 0x0404 was not read")
	}
	// exiftool -a -G1 -IPTC:all: Keywords "alpha, beta", By-line "Fixture
	// Author", Copyright Notice "(c) 2026 Fixture", Caption-Abstract
	// "ExifTool raw profile caption", Application Record Version 4. Listed in
	// stream order (IPTC IIM 4.2 §6: 2:25, 2:80, 2:116, 2:120). The record
	// version 2:00 is a structural marker that iptc.Parse does not store in
	// Records (IIM §2.2.1); it is checked in the raw stream below.
	want := []struct {
		ds    uint8
		value string
	}{
		{25, "alpha"}, {25, "beta"}, {80, "Fixture Author"},
		{116, "(c) 2026 Fixture"}, {120, "ExifTool raw profile caption"},
	}
	if !bytes.Contains(m.rawIPTC, []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04}) {
		t.Error("raw IIM lacks Application Record Version 2:00 = 4")
	}
	got := m.IPTC.Records[2]
	if len(got) != len(want) {
		t.Fatalf("record 2 holds %d datasets, ExifTool reports %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].DataSet != w.ds || string(got[i].Value) != w.value {
			t.Errorf("dataset %d = 2:%d %q, want 2:%d %q", i, got[i].DataSet, got[i].Value, w.ds, w.value)
		}
	}
	if m.Caption() != "ExifTool raw profile caption" || m.Copyright() != "(c) 2026 Fixture" {
		t.Errorf("Caption/Copyright = %q / %q", m.Caption(), m.Copyright())
	}
	// The fixture's digest matches its IIM, so IPTC trust is not elevated.
	if m.rawIPTCDigest == nil || m.iptcTrustElev {
		t.Errorf("digest %x, trust elevated %v; want the 0x0425 digest and false", m.rawIPTCDigest, m.iptcTrustElev)
	}
}

// TestRead_TIFFPaddedIPTCDigestIsCurrent (task #301): Photoshop and ExifTool
// hash tag 0x83BB with its LONG zero padding; the Toolkit accepts that form
// (TIFF_Handler.cpp:ProcessXMP), so such a digest does not elevate IPTC.
func TestRead_TIFFPaddedIPTCDigestIsCurrent(t *testing.T) {
	t.Parallel()
	// 14 bytes: tag 0x83BB of type LONG stores it with 2 zero pad bytes.
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'c', 'a', 'p', 0x1C, 0x02, 0x74, 0x00, 0x01, 'c'}
	for name, digest := range map[string][16]byte{
		"padded":   irb.Digest(iim, 4),
		"unpadded": irb.Digest(iim, 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := tiffWithIPTCAndDigest(t, iim, digest[:])
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.rawIPTCDigest == nil {
				t.Fatal("digest not read from tag 0x8649")
			}
			if m.iptcTrustElev {
				t.Error("a current digest elevated IPTC trust")
			}
		})
	}
	t.Run("stale", func(t *testing.T) {
		t.Parallel()
		m, err := Read(bytes.NewReader(tiffWithIPTCAndDigest(t, iim, bytes.Repeat([]byte{1}, 16))))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !m.iptcTrustElev {
			t.Error("a stale digest did not elevate IPTC trust (MWG §4.2.3.2)")
		}
	})
}

// tiffWithIPTCAndDigest encodes a TIFF whose IFD0 carries iim in tag 0x83BB
// (LONG, zero padded) and an IRB with digest in tag 0x8649.
func tiffWithIPTCAndDigest(t *testing.T, iim, digest []byte) []byte {
	t.Helper()
	psir := irb.AppendBlock(nil, irb.ResourceIPTCDigest, digest)
	padded := append(bytes.Clone(iim), make([]byte, (4-len(iim)%4)%4)...)
	e := &exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{
		{Tag: exif.TagIPTC, Type: exif.TypeLong, Count: uint32(len(padded) / 4), Value: padded},              //nolint:gosec // G115: test sizes are small
		{Tag: exif.TagID(tiffscan.PSIRTag), Type: exif.TypeUndefined, Count: uint32(len(psir)), Value: psir}, //nolint:gosec // G115: test sizes are small
	}}}
	out, err := exif.Encode(e)
	if err != nil {
		t.Fatalf("exif.Encode: %v", err)
	}
	return out
}

// jpegWithAPP13 inserts an APP13 "Photoshop 3.0" segment holding irbData
// right after the SOI marker of the committed jolla.jpg fixture.
func jpegWithAPP13(t *testing.T, irbData []byte) []byte {
	t.Helper()
	base, err := os.ReadFile("testdata/fixtures/jolla.jpg")
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("Photoshop 3.0\x00"), irbData...)
	seg := []byte{0xFF, 0xED}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2)) //nolint:gosec // G115: test sizes are small
	seg = append(seg, payload...)
	out := append(append(bytes.Clone(base[:2]), seg...), base[2:]...)
	return out
}

// writeCaption reads data, sets the caption (and, through it, the IPTC
// 2:120 dataset) and writes the result.
func writeCaption(t *testing.T, data []byte, caption string) []byte {
	t.Helper()
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption(caption)
	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.Bytes()
}

// TestWrite_JPEGIPTCDigestRefreshed (task #284): after an IPTC write the
// APP13 digest equals the MD5 of the written IIM block, and the written file
// reads back with XMP priority (digest current, MWG §4.2.3.2).
func TestWrite_JPEGIPTCDigestRefreshed(t *testing.T) {
	t.Parallel()
	i := &iptc.IPTC{}
	i.SetCaption("before")
	oldIIM, err := iptc.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := md5.Sum(oldIIM) //nolint:gosec // G401: MWG digest
	src := jpegWithAPP13(t, append(irb.AppendBlock(nil, irb.ResourceIPTCDigest, oldDigest[:]), irb.AppendBlock(nil, irb.ResourceIPTC, oldIIM)...))

	out := writeCaption(t, src, "after")
	_, rawIPTC, digest, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ExtractFull: %v", err)
	}
	want := md5.Sum(rawIPTC) //nolint:gosec // G401: MWG digest
	if !bytes.Equal(digest, want[:]) {
		t.Errorf("digest = %x, want MD5 of the written IIM %x", digest, want)
	}
	m, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if m.iptcTrustElev || m.Caption() != "after" {
		t.Errorf("read-back: trust elevated %v, caption %q", m.iptcTrustElev, m.Caption())
	}
}

// TestWrite_JPEGIPTCDigestCreated (task #323, MWG Guidelines v2.0 §4.2.3.2):
// a file whose IRB has no 0x0425 resource gets one when IPTC and XMP are
// written together, holding the MD5 of the written IIM, and reads back with
// XMP priority (digest current).
func TestWrite_JPEGIPTCDigestCreated(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/fixtures/IPTC-PhotometadataRef-Std2021.1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, d, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(data)); err != nil || d != nil {
		t.Fatalf("fixture precondition: digest %x, err %v", d, err)
	}
	out := writeCaption(t, data, "digest please")
	_, rawIPTC, digest, rawXMP, _, _, err := jpeg.ExtractFull(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ExtractFull: %v", err)
	}
	if rawIPTC == nil || rawXMP == nil {
		t.Fatalf("IPTC (%v) or XMP (%v) lost on write", rawIPTC != nil, rawXMP != nil)
	}
	want := md5.Sum(rawIPTC) //nolint:gosec // G401: MWG digest
	if !bytes.Equal(digest, want[:]) {
		t.Errorf("digest = %x, want MD5 of the written IIM %x", digest, want)
	}
	m, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if m.iptcTrustElev || m.Caption() != "digest please" {
		t.Errorf("read-back: trust elevated %v, caption %q", m.iptcTrustElev, m.Caption())
	}
}

// TestWrite_JPEGStaleIPTCDigestStaysStale (task #323): a digest that did not
// match the IIM before the write still does not match it afterwards, so MWG
// consumers keep detecting the out-of-sync IIM, and the written caption wins
// on read (internal/reconcile: with a stale digest the IIM value wins).
func TestWrite_JPEGStaleIPTCDigestStaysStale(t *testing.T) {
	t.Parallel()
	i := &iptc.IPTC{}
	i.SetCaption("before")
	oldIIM, err := iptc.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	stale := bytes.Repeat([]byte{0x5A}, 16)
	src := jpegWithAPP13(t, append(irb.AppendBlock(nil, irb.ResourceIPTCDigest, stale), irb.AppendBlock(nil, irb.ResourceIPTC, oldIIM)...))

	out := writeCaption(t, src, "after")
	_, rawIPTC, digest, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ExtractFull: %v", err)
	}
	if !bytes.Equal(digest, stale) {
		t.Errorf("digest = %x, want the stale digest %x kept", digest, stale)
	}
	if cur := md5.Sum(rawIPTC); bytes.Equal(digest, cur[:]) { //nolint:gosec // G401: MWG digest
		t.Error("the stale digest became current")
	}
	m, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if !m.iptcTrustElev || m.Caption() != "after" {
		t.Errorf("read-back: trust elevated %v, caption %q", m.iptcTrustElev, m.Caption())
	}
}

// TestWrite_TIFFIPTCDigestRefreshed (task #284): the TIFF fixture with IPTC
// and digest only in tag 0x8649 gets tag 0x83BB on write, and the digest
// matches the stored tag 0x83BB value including its LONG padding.
func TestWrite_TIFFIPTCDigestRefreshed(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(psirOnlyTIFF)
	if err != nil {
		t.Fatal(err)
	}
	out := writeCaption(t, data, "after")
	e, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse output: %v", err)
	}
	native := e.IFD0.Get(exif.TagIPTC)
	if native == nil {
		t.Fatal("tag 0x83BB missing after the IPTC write")
	}
	want := md5.Sum(native.Value[:4*native.Count]) //nolint:gosec // G401: MWG digest
	if got := tiffscan.IPTCDigest(out); !bytes.Equal(got, want[:]) {
		t.Errorf("digest = %x, want MD5 of the padded tag 0x83BB value %x", got, want)
	}
	m, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if m.iptcTrustElev || m.Caption() != "after" {
		t.Errorf("read-back: trust elevated %v, caption %q", m.iptcTrustElev, m.Caption())
	}
}
