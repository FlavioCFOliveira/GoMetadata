package tiff

// iptc_digest_test.go — regression tests for task #284 in the TIFF family:
// after an IPTC write into tag 0x83BB, the IPTC digest (resource 0x0425 of the
// Photoshop image resource block in tag 0x8649) equals the MD5 of the stored
// tag 0x83BB value, LONG zero padding included (MWG Guidelines v2.0 §3.3.1;
// the bytes ExifTool hashes for "IPTCDigest is not current").

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest algorithm, not used for security
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
)

// psirWith builds a Photoshop image resource block holding the given IIM
// (resource 0x0404, omitted when nil) and digest (0x0425, omitted when nil).
func psirWith(iim, digest []byte) []byte {
	var b []byte
	b = irb.AppendBlock(b, 0x040C, []byte("thumbnail"))
	if digest != nil {
		b = irb.AppendBlock(b, irb.ResourceIPTCDigest, digest)
	}
	if iim != nil {
		b = irb.AppendBlock(b, irb.ResourceIPTC, iim)
	}
	return b
}

// ifdWith returns an IFD0 holding tag 0x83BB (when native is non-nil) and
// tag 0x8649 (psir), sorted by tag.
func ifdWith(native, psir []byte) *exif.IFD {
	ifd := &exif.IFD{}
	if native != nil {
		upsertIFD0Entry(ifd, exif.TagIPTC, exif.TypeLong, native)
	}
	upsertIFD0Entry(ifd, tagPhotoshopIRB, exif.TypeUndefined, psir)
	return ifd
}

func paddedMD5(iim []byte) []byte {
	p := append(bytes.Clone(iim), make([]byte, (4-len(iim)%4)%4)...)
	s := md5.Sum(p) //nolint:gosec // G401: MWG digest
	return s[:]
}

func TestUpsertIPTC_DigestPolicy(t *testing.T) {
	t.Parallel()
	oldIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}      // 8 bytes, aligned
	newIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'n', 'e', 'w', '!'} // 9 bytes, padded to 12
	stale := bytes.Repeat([]byte{0xEE}, 16)
	oldMD5 := md5.Sum(oldIIM) //nolint:gosec // G401: MWG digest

	cases := []struct {
		name    string
		ifd     *exif.IFD
		newIPTC []byte
		want    []byte // expected digest; nil: no digest resource
	}{
		{"changed IIM refreshes", ifdWith(oldIIM, psirWith(nil, oldMD5[:])), newIIM, paddedMD5(newIIM)},
		{"changed IIM, stale digest refreshes", ifdWith(oldIIM, psirWith(nil, stale)), newIIM, paddedMD5(newIIM)},
		{"IIM only in 0x8649, changed", ifdWith(nil, psirWith(oldIIM, oldMD5[:])), newIIM, paddedMD5(newIIM)},
		{"unchanged IIM, current digest kept current", ifdWith(newIIM, psirWith(nil, paddedMD5(newIIM))), newIIM, paddedMD5(newIIM)},
		{"unchanged IIM, unpadded digest rewritten padded", ifdWith(nil, psirWith(newIIM, md5Sum(newIIM))), newIIM, paddedMD5(newIIM)},
		{"unchanged IIM, stale digest kept stale", ifdWith(oldIIM, psirWith(nil, stale)), oldIIM, stale},
		{"no digest stays without one", ifdWith(oldIIM, psirWith(nil, nil)), newIIM, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := bytes.Clone(tc.ifd.Get(tagPhotoshopIRB).Value)
			aliased := tc.ifd.Get(tagPhotoshopIRB).Value
			upsertIPTC(tc.ifd, tc.newIPTC)
			if got := tc.ifd.Get(exif.TagIPTC); got == nil || !bytes.Equal(got.Value, tc.newIPTC) {
				t.Fatalf("tag 0x83BB not stored")
			}
			psir := tc.ifd.Get(tagPhotoshopIRB).Value
			if got := irb.FindDigest(psir); !bytes.Equal(got, tc.want) {
				t.Errorf("digest = %x, want %x", got, tc.want)
			}
			if !bytes.Equal(aliased, before) {
				t.Error("the original 0x8649 value was modified in place")
			}
			if len(psir) != len(before) {
				t.Errorf("0x8649 length changed: %d -> %d", len(before), len(psir))
			}
		})
	}
	// No 0x8649 at all: plain upsert.
	ifd := &exif.IFD{}
	upsertIPTC(ifd, newIIM)
	if ifd.Get(tagPhotoshopIRB) != nil || ifd.Get(exif.TagIPTC) == nil {
		t.Error("upsertIPTC without an IRB must only store tag 0x83BB")
	}
}

func md5Sum(b []byte) []byte {
	s := md5.Sum(b) //nolint:gosec // G401: MWG digest
	return s[:]
}

// TestInject_PSIRDigestRefreshed (task #284, end to end): the committed
// fixture holds IPTC and a current digest only in tag 0x8649; after an IPTC
// write the digest equals the MD5 of the written tag 0x83BB value.
func TestInject_PSIRDigestRefreshed(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "tiff_iptc_in_psir_only.tif"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	newIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	var out bytes.Buffer
	if err := InjectWithEXIFStream(bytes.NewReader(data), data, true, e, newIIM, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIFStream: %v", err)
	}
	got, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("Parse output: %v", err)
	}
	native := got.IFD0.Get(exif.TagIPTC)
	psir := got.IFD0.Get(tagPhotoshopIRB)
	if native == nil || psir == nil {
		t.Fatal("output lost tag 0x83BB or 0x8649")
	}
	stored := native.Value[:4*native.Count]
	want := md5.Sum(stored) //nolint:gosec // G401: MWG digest
	if d := irb.FindDigest(psir.Value); !bytes.Equal(d, want[:]) {
		t.Errorf("digest = %x, want MD5(tag 0x83BB value) %x", d, want)
	}
	if binary.LittleEndian.Uint32(out.Bytes()[4:]) == 0 {
		t.Error("output has no IFD0")
	}
}
