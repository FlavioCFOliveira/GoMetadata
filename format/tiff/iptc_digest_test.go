package tiff

// iptc_digest_test.go — regression tests for task #284 in the TIFF family:
// after an IPTC write into tag 0x83BB, the IPTC digest (resource 0x0425 of the
// Photoshop image resource block in tag 0x8649) equals the MD5 of the stored
// tag 0x83BB value, LONG zero padding included (MWG Guidelines v2.0 §4.2.3.2;
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

	xmpOn := []byte("<x/>")
	cases := []struct {
		name    string
		ifd     *exif.IFD
		newIPTC []byte
		rawXMP  []byte // XMP written in the same pass (nil: none)
		want    []byte // expected digest; nil: no digest resource
	}{
		{"RECONCILE-06 changed IIM, matching digest refreshes", ifdWith(oldIIM, psirWith(nil, oldMD5[:])), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 changed IIM, stale digest refreshes", ifdWith(oldIIM, psirWith(nil, stale)), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 IIM only in 0x8649, changed", ifdWith(nil, psirWith(oldIIM, oldMD5[:])), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 unchanged IIM, current digest kept current", ifdWith(newIIM, psirWith(nil, paddedMD5(newIIM))), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 unchanged IIM, unpadded digest rewritten padded", ifdWith(nil, psirWith(newIIM, md5Sum(newIIM))), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 unchanged IIM, stale digest refreshes", ifdWith(oldIIM, psirWith(nil, stale)), oldIIM, xmpOn, paddedMD5(oldIIM)},
		{"RECONCILE-06 no XMP: matching digest kept as it is", ifdWith(oldIIM, psirWith(nil, oldMD5[:])), newIIM, nil, oldMD5[:]},
		{"RECONCILE-06 no XMP: stale digest kept as it is", ifdWith(oldIIM, psirWith(nil, stale)), newIIM, nil, stale},
		{"RECONCILE-06 no digest, no XMP: none created", ifdWith(oldIIM, psirWith(nil, nil)), newIIM, nil, nil},
		{"RECONCILE-06 no digest, XMP written: created", ifdWith(oldIIM, psirWith(nil, nil)), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 no digest, XMP in IFD0: created", ifdWithXMP(ifdWith(oldIIM, psirWith(nil, nil))), newIIM, nil, paddedMD5(newIIM)},
		{"RECONCILE-06 stale digest, XMP in IFD0: refreshed", ifdWithXMP(ifdWith(oldIIM, psirWith(nil, stale))), newIIM, nil, paddedMD5(newIIM)},
		{"RECONCILE-06 no digest, IIM copy in 0x8649, XMP written: created", ifdWith(nil, psirWith(oldIIM, nil)), newIIM, xmpOn, paddedMD5(newIIM)},
		{"RECONCILE-06 no digest, 0x8649 IIM equals new IIM, XMP written: created without a second IIM", ifdWith(nil, psirWith(newIIM, nil)), newIIM, xmpOn, paddedMD5(newIIM)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := bytes.Clone(tc.ifd.Get(tagPhotoshopIRB).Value)
			aliased := tc.ifd.Get(tagPhotoshopIRB).Value
			upsertIPTC(tc.ifd, tc.newIPTC, tc.rawXMP)
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
			// Task #314: a 0x0404 copy of the IIM in 0x8649 is rewritten with
			// the new IIM; a block without one keeps its length (only the
			// digest changes in place).
			switch {
			case irb.FindIPTC(before) != nil:
				if got := irb.FindIPTC(psir); !bytes.Equal(got, tc.newIPTC) {
					t.Errorf("0x8649 IIM copy = %x, want %x", got, tc.newIPTC)
				}
			case tc.want != nil && irb.FindDigest(before) == nil:
				if len(psir) != len(before)+12+irb.DigestSize {
					t.Errorf("0x8649 length %d -> %d, want one added digest resource", len(before), len(psir))
				}
			case len(psir) != len(before):
				t.Errorf("0x8649 length changed: %d -> %d", len(before), len(psir))
			}
		})
	}
	// No 0x8649 at all and no XMP: plain upsert.
	ifd := &exif.IFD{}
	upsertIPTC(ifd, newIIM, nil)
	if ifd.Get(tagPhotoshopIRB) != nil || ifd.Get(exif.TagIPTC) == nil {
		t.Error("upsertIPTC without an IRB or XMP must only store tag 0x83BB")
	}
	// No 0x8649 but XMP written: a new block holding only the digest (task #323).
	ifd = &exif.IFD{}
	upsertIPTC(ifd, newIIM, []byte("<x/>"))
	p := ifd.Get(tagPhotoshopIRB)
	if p == nil || p.Type != exif.TypeByte || int(p.Count) != len(p.Value) {
		t.Fatalf("upsertIPTC with XMP did not create a byte-typed 0x8649: %+v", p)
	}
	if d := irb.FindDigest(p.Value); !bytes.Equal(d, paddedMD5(newIIM)) || irb.FindIPTC(p.Value) != nil {
		t.Errorf("created block: digest %x (want %x), IIM %x (want none)", d, paddedMD5(newIIM), irb.FindIPTC(p.Value))
	}
}

// ifdWithXMP adds an XMP entry (tag 0x02BC) to ifd.
func ifdWithXMP(ifd *exif.IFD) *exif.IFD {
	upsertIFD0Entry(ifd, exif.TagXMP, exif.TypeByte, []byte("<x/>"))
	return ifd
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
	if err := InjectWithEXIFStream(bytes.NewReader(data), data, true, e, newIIM, []byte("<x/>"), &out); err != nil {
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
