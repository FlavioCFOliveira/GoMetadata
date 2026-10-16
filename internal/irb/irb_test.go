package irb

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest algorithm, not used for security
	"encoding/binary"
	"testing"
)

// block builds one resource with an optional Pascal name, padded as
// Photoshop writes it.
func block(id uint16, name string, data []byte) []byte {
	b := []byte("8BIM")
	b = binary.BigEndian.AppendUint16(b, id)
	b = append(b, byte(len(name))) //nolint:gosec // G115: test names are shorter than 256 bytes
	b = append(b, name...)
	if (len(name)+1)%2 != 0 {
		b = append(b, 0)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(data))) //nolint:gosec // G115: test sizes are small
	b = append(b, data...)
	if len(data)%2 != 0 {
		b = append(b, 0)
	}
	return b
}

func md5Of(b []byte) []byte {
	s := md5.Sum(b) //nolint:gosec // G401: MWG digest
	return s[:]
}

var (
	iimOld = []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}           //nolint:gochecknoglobals // read-only test fixture
	iimNew = []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'n', 'e', 'w', '!', '!'} //nolint:gochecknoglobals // read-only test fixture
)

func TestSkipPascalString_EdgeCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		b       []byte
		pos     int
		wantPos int
		wantOK  bool
	}{
		{"pos at end", []byte{0x05}, 1, 2, false},
		{"empty name", []byte{0x00, 0x00}, 0, 2, true},
		{"odd total, padded", []byte{0x02, 'A', 'B', 0x00}, 0, 4, true},
		{"even total, no pad", []byte{0x01, 'A'}, 0, 2, true},
		{"name overruns", []byte{0x09, 'A'}, 0, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pos, ok := skipPascalString(tc.b, tc.pos)
			if pos != tc.wantPos || ok != tc.wantOK {
				t.Errorf("skipPascalString = (%d, %v), want (%d, %v)", pos, ok, tc.wantPos, tc.wantOK)
			}
		})
	}
}

func TestParseEntry(t *testing.T) {
	t.Parallel()
	b := block(ResourceIPTC, "ab", iimOld)
	id, data, newPos, ok := ParseEntry(b, 0)
	if !ok || id != ResourceIPTC || !bytes.Equal(data, iimOld) || newPos != 4+2+4+4+len(iimOld) {
		t.Fatalf("ParseEntry = (%#x, %x, %d, %v)", id, data, newPos, ok)
	}

	// Recoverable miss: bad signature or overflowing name -> newPos == pos.
	if _, _, p, ok := ParseEntry([]byte("XBIM\x04\x04\x00\x00\x00\x00\x00\x00"), 0); ok || p != 0 {
		t.Errorf("bad signature: newPos %d ok %v, want 0 false", p, ok)
	}
	if _, _, p, ok := ParseEntry([]byte("8BIM\x04\x04\xFF"), 0); ok || p != 0 {
		t.Errorf("overflowing name: newPos %d ok %v, want 0 false", p, ok)
	}
	// Terminal failures -> newPos > pos.
	for name, in := range map[string][]byte{
		"short signature": []byte("8BI"),
		"no id":           []byte("8BIM\x04"),
		"no size":         []byte("8BIM\x04\x04\x00\x00\x00"),
		"data overruns":   []byte("8BIM\x04\x04\x00\x00\x00\x00\x00\x09ab"),
		"size 4 GiB - 1":  []byte("8BIM\x04\x04\x00\x00\xFF\xFF\xFF\xFFab"),
	} {
		if _, _, p, ok := ParseEntry(in, 0); ok || p <= 0 {
			t.Errorf("%s: newPos %d ok %v, want > 0 false", name, p, ok)
		}
	}
}

func TestFindIPTCAndDigest(t *testing.T) {
	t.Parallel()
	dig := md5Of(iimOld)
	stream := append(append(append([]byte("junk"), block(0x040C, "", []byte("thumb"))...),
		block(ResourceIPTCDigest, "", dig)...), block(ResourceIPTC, "x", iimOld)...)

	if got := FindIPTC(stream); !bytes.Equal(got, iimOld) {
		t.Errorf("FindIPTC = %x, want %x", got, iimOld)
	}
	iim, d := IPTCAndDigest(stream)
	if !bytes.Equal(iim, iimOld) || !bytes.Equal(d, dig) {
		t.Errorf("IPTCAndDigest = %x, %x", iim, d)
	}
	if got := FindDigest(stream); !bytes.Equal(got, dig) {
		t.Errorf("FindDigest = %x, want %x", got, dig)
	}
	// A 0x0425 resource that is not 16 bytes is ignored (the digest has 16 bytes: MWG §4.2.3.2, p. 26).
	bad := block(ResourceIPTCDigest, "", dig[:15])
	if _, d := IPTCAndDigest(bad); d != nil {
		t.Errorf("15-byte digest accepted: %x", d)
	}
	if FindDigest(bad) != nil || FindIPTC(bad) != nil {
		t.Error("FindDigest/FindIPTC returned data for a stream without them")
	}
	if FindIPTC(nil) != nil || FindDigest(nil) != nil {
		t.Error("nil stream returned data")
	}
}

func TestDigest(t *testing.T) {
	t.Parallel()
	odd := []byte{0x1C, 0x02, 0x78, 0x00, 0x01, 'x'} // 6 bytes
	padded := append(bytes.Clone(odd), 0, 0)
	if d := Digest(odd, 1); !bytes.Equal(d[:], md5Of(odd)) {
		t.Error("Digest(align 1) is not MD5 of the data")
	}
	if d := Digest(odd, 4); !bytes.Equal(d[:], md5Of(padded)) {
		t.Error("Digest(align 4) is not MD5 of the zero-padded data")
	}
	if d := Digest(padded, 4); !bytes.Equal(d[:], md5Of(padded)) {
		t.Error("Digest(align 4) padded an already aligned value")
	}
	if !DigestCurrent(odd, md5Of(odd), 4) || !DigestCurrent(odd, md5Of(padded), 4) {
		t.Error("DigestCurrent rejected the unpadded or padded digest")
	}
	if DigestCurrent(odd, md5Of(padded), 1) {
		t.Error("DigestCurrent(align 1) accepted the padded digest")
	}
	if DigestCurrent(odd, md5Of([]byte("other")), 4) || DigestCurrent(odd, nil, 4) {
		t.Error("DigestCurrent accepted a wrong or missing digest")
	}
}

func TestRefreshDigest(t *testing.T) {
	t.Parallel()
	orig := append(block(ResourceIPTCDigest, "", md5Of(iimOld)), block(ResourceIPTC, "", iimOld)...)
	keep := bytes.Clone(orig)
	want := Digest(iimNew, 4)
	got := RefreshDigest(orig, want)
	if !bytes.Equal(orig, keep) {
		t.Fatal("RefreshDigest modified its input")
	}
	if d := FindDigest(got); !bytes.Equal(d, want[:]) {
		t.Errorf("digest after refresh = %x, want %x", d, want)
	}
	if len(got) != len(orig) || !bytes.Equal(FindIPTC(got), iimOld) {
		t.Error("RefreshDigest changed something other than the digest")
	}
	if RefreshDigest(block(ResourceIPTC, "", iimOld), want) != nil {
		t.Error("RefreshDigest created a digest where none existed")
	}
}

// TestAppendSpliced_DigestPolicy pins the digest rules of the IRB splice
// (tasks #284 and #323; MWG Guidelines v2.0 §4.2.3.2, p. 29): when IIM and XMP
// are both written, the digest is created when absent and rewritten to the MD5
// of the IIM written when present, a stale one included, whether the IIM
// changes or not; without XMP, or when the IIM is removed, digests are kept.
func TestAppendSpliced_DigestPolicy(t *testing.T) {
	t.Parallel()
	stale := bytes.Repeat([]byte{0xAB}, DigestSize)
	cases := []struct {
		name       string
		orig       []byte
		newIPTC    []byte
		withXMP    bool
		wantDigest []byte // nil: no 0x0425 resource expected
		wantIPTC   []byte
	}{
		{
			name:       "RECONCILE-06 matching digest is refreshed",
			orig:       append(block(ResourceIPTCDigest, "", md5Of(iimOld)), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 matching digest after the IPTC resource is refreshed",
			orig:       append(block(ResourceIPTC, "", iimOld), block(ResourceIPTCDigest, "", md5Of(iimOld))...),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 stale digest is refreshed when the IPTC changes",
			orig:       append(block(ResourceIPTCDigest, "", stale), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 stale digest is refreshed when the IPTC is unchanged",
			orig:       append(block(ResourceIPTCDigest, "", stale), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    iimOld,
			withXMP:    true,
			wantDigest: md5Of(iimOld),
			wantIPTC:   iimOld,
		},
		{
			name:       "RECONCILE-06 current digest of an unchanged IPTC stays",
			orig:       append(block(ResourceIPTCDigest, "", md5Of(iimOld)), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    iimOld,
			withXMP:    true,
			wantDigest: md5Of(iimOld),
			wantIPTC:   iimOld,
		},
		{
			name:       "RECONCILE-06 digest without IIM is refreshed to the IIM written",
			orig:       block(ResourceIPTCDigest, "", stale),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 absent digest is created beside an existing IIM",
			orig:       block(ResourceIPTC, "", iimOld),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 absent digest is created when the whole IRB is new",
			orig:       nil,
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 absent digest is created beside sibling resources",
			orig:       block(0x040C, "", []byte("thumb")),
			newIPTC:    iimNew,
			withXMP:    true,
			wantDigest: md5Of(iimNew),
			wantIPTC:   iimNew,
		},
		{
			name:     "RECONCILE-06 absent digest is not created without XMP",
			orig:     block(ResourceIPTC, "", iimOld),
			newIPTC:  iimNew,
			wantIPTC: iimNew,
		},
		{
			name:       "RECONCILE-06 digest is kept as it is without XMP",
			orig:       append(block(ResourceIPTCDigest, "", stale), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    iimNew,
			wantDigest: stale,
			wantIPTC:   iimNew,
		},
		{
			name:       "RECONCILE-06 IPTC removed keeps digest",
			orig:       append(block(ResourceIPTCDigest, "", md5Of(iimOld)), block(ResourceIPTC, "", iimOld)...),
			newIPTC:    nil,
			withXMP:    true,
			wantDigest: md5Of(iimOld),
		},
		{
			name:     "RECONCILE-06 IPTC removed creates no digest",
			orig:     block(ResourceIPTC, "", iimOld),
			newIPTC:  nil,
			withXMP:  true,
			wantIPTC: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := AppendSpliced(nil, tc.orig, tc.newIPTC, tc.withXMP)
			iim, d := IPTCAndDigest(out)
			if !bytes.Equal(d, tc.wantDigest) {
				t.Errorf("digest = %x, want %x", d, tc.wantDigest)
			}
			if !bytes.Equal(iim, tc.wantIPTC) {
				t.Errorf("IPTC = %x, want %x", iim, tc.wantIPTC)
			}
			if n := bytes.Count(out, []byte("8BIM\x04\x25")); n > 1 {
				t.Errorf("%d digest resources, want at most one", n)
			}
		})
	}
}

// TestAppendSpliced_PreservesSiblings: every other resource is copied
// verbatim, including its Pascal name and pad byte.
func TestAppendSpliced_PreservesSiblings(t *testing.T) {
	t.Parallel()
	sib1 := block(0x040C, "thumbnail", []byte("abc"))
	sib2 := block(0x0421, "", []byte("version"))
	orig := append(append(bytes.Clone(sib1), block(ResourceIPTC, "", iimOld)...), sib2...)
	out := AppendSpliced(nil, orig, iimNew, false)
	want := append(append(bytes.Clone(sib1), AppendBlock(nil, ResourceIPTC, iimNew)...), sib2...)
	if !bytes.Equal(out, want) {
		t.Errorf("spliced IRB\n got %x\nwant %x", out, want)
	}
}

func TestAppendBlock_OddPadding(t *testing.T) {
	t.Parallel()
	got := AppendBlock(nil, ResourceIPTC, []byte{1, 2, 3})
	want := []byte{'8', 'B', 'I', 'M', 0x04, 0x04, 0, 0, 0, 0, 0, 3, 1, 2, 3, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("AppendBlock = %x, want %x", got, want)
	}
}

// FuzzIRB feeds arbitrary bytes to every parser in the package: none may
// panic, every returned slice must alias the input, and RefreshDigest must
// never change the length of the stream.
func FuzzIRB(f *testing.F) {
	f.Add(append(block(ResourceIPTCDigest, "", md5Of(iimOld)), block(ResourceIPTC, "n", iimOld)...))
	f.Add([]byte("8BIM\x04\x04\xFF"))
	f.Add([]byte("8BIM\x04\x25\x00\x00\xFF\xFF\xFF\xFF"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = FindIPTC(b)
		_, _ = IPTCAndDigest(b)
		_ = FindDigest(b)
		if r := RefreshDigest(b, Digest(iimNew, 4)); r != nil && len(r) != len(b) {
			t.Fatalf("RefreshDigest changed the length: %d -> %d", len(b), len(r))
		}
		_ = AppendSpliced(nil, b, iimNew, true)
		_ = AppendSpliced(nil, b, nil, true)
	})
}

func BenchmarkIPTCAndDigest(b *testing.B) {
	stream := append(append(block(0x040C, "", bytes.Repeat([]byte{1}, 4096)),
		block(ResourceIPTCDigest, "", md5Of(iimOld))...), block(ResourceIPTC, "", iimOld)...)
	b.ReportAllocs()
	for range b.N {
		_, _ = IPTCAndDigest(stream)
	}
}

func BenchmarkAppendSpliced(b *testing.B) {
	orig := append(append(block(0x040C, "", bytes.Repeat([]byte{1}, 4096)),
		block(ResourceIPTCDigest, "", md5Of(iimOld))...), block(ResourceIPTC, "", iimOld)...)
	dst := make([]byte, 0, len(orig)+64)
	b.ReportAllocs()
	for range b.N {
		dst = AppendSpliced(dst[:0], orig, iimNew, true)
	}
}
