package irb

// irb_task313_test.go — regression tests for task #313: duplicate resources
// are resolved by one policy in every reader and in the splice, the XMP
// Toolkit's (PSIR_FileWriter.cpp:PSIR_FileWriter::ParseMemoryResources): the
// first resource of an ID wins, except that a later non-empty copy replaces
// an empty first one. Before the fix IPTCAndDigest (the JPEG reader) kept the
// last 0x0404 resource and FindIPTC (the TIFF reader) the first.

import (
	"bytes"
	"testing"
)

func TestDuplicateResources_FirstWins(t *testing.T) {
	t.Parallel()
	iimA := []byte{0x1C, 0x02, 0x78, 0x00, 0x01, 'A'}
	iimB := []byte{0x1C, 0x02, 0x78, 0x00, 0x01, 'B'}
	digA := bytes.Repeat([]byte{0xAA}, DigestSize)
	digB := bytes.Repeat([]byte{0xBB}, DigestSize)
	cat := func(blocks ...[]byte) []byte { return bytes.Join(blocks, nil) }
	cases := []struct {
		name       string
		stream     []byte
		wantIPTC   []byte
		wantDigest []byte
	}{
		{"two copies: first wins", cat(block(ResourceIPTC, "", iimA), block(ResourceIPTC, "", iimB)), iimA, nil},
		{"empty first copy: later non-empty wins", cat(block(ResourceIPTC, "", nil), block(ResourceIPTC, "", iimB)), iimB, nil},
		{"empty later copy is ignored", cat(block(ResourceIPTC, "", iimA), block(ResourceIPTC, "", nil)), iimA, nil},
		{"three copies, other resources between", cat(block(ResourceIPTC, "x", iimA), block(0x040C, "", []byte("thumb")), block(ResourceIPTC, "", iimB), block(ResourceIPTC, "", iimB)), iimA, nil},
		{"two digests: first wins", cat(block(ResourceIPTCDigest, "", digA), block(ResourceIPTC, "", iimA), block(ResourceIPTCDigest, "", digB)), iimA, digA},
		{"short digest skipped", cat(block(ResourceIPTCDigest, "", digA[:15]), block(ResourceIPTCDigest, "", digB)), nil, digB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := FindIPTC(tc.stream); !bytes.Equal(got, tc.wantIPTC) {
				t.Errorf("FindIPTC (TIFF path) = %x, want %x", got, tc.wantIPTC)
			}
			iim, digest := IPTCAndDigest(tc.stream)
			if !bytes.Equal(iim, tc.wantIPTC) {
				t.Errorf("IPTCAndDigest (JPEG path) IPTC = %x, want %x", iim, tc.wantIPTC)
			}
			if !bytes.Equal(digest, tc.wantDigest) {
				t.Errorf("IPTCAndDigest digest = %x, want %x", digest, tc.wantDigest)
			}
			if got := FindDigest(tc.stream); !bytes.Equal(got, tc.wantDigest) {
				t.Errorf("FindDigest = %x, want %x", got, tc.wantDigest)
			}
		})
	}
	// An empty-only stream yields the (empty) first copy, not nil data from a
	// missing resource: both readers agree.
	only := cat(block(ResourceIPTC, "", nil), block(ResourceIPTC, "", nil))
	a, b := FindIPTC(only), iptcOf(only)
	if a == nil || b == nil || len(a) != 0 || len(b) != 0 {
		t.Errorf("empty copies: FindIPTC %v, IPTCAndDigest %v; want empty, non-nil", a, b)
	}
}

// iptcOf returns the IPTC result of IPTCAndDigest.
func iptcOf(b []byte) []byte {
	iim, _ := IPTCAndDigest(b)
	return iim
}

// TestAppendSpliced_Duplicates (tasks #313, #314): the new IIM takes the place
// of the copy the policy selects, the other 0x0404 copies are dropped (the
// Toolkit writer keeps one resource per ID), every other resource is kept,
// and a nil IIM removes every copy.
func TestAppendSpliced_Duplicates(t *testing.T) {
	t.Parallel()
	thumb := block(0x040C, "", []byte("thumbnail"))
	orig := bytes.Join([][]byte{
		block(ResourceIPTC, "", nil),
		thumb,
		block(ResourceIPTC, "nm", iimOld),
		block(ResourceIPTC, "", iimOld),
	}, nil)
	out := AppendSpliced(nil, orig, iimNew, false)
	want := bytes.Join([][]byte{thumb, AppendBlock(nil, ResourceIPTC, iimNew)}, nil)
	if !bytes.Equal(out, want) {
		t.Fatalf("AppendSpliced =\n%x\nwant\n%x", out, want)
	}
	if got := FindIPTC(out); !bytes.Equal(got, iimNew) {
		t.Errorf("FindIPTC(out) = %x", got)
	}
	if removed := AppendSpliced(nil, orig, nil, false); !bytes.Equal(removed, thumb) {
		t.Errorf("AppendSpliced(nil IIM) = %x, want only the thumbnail resource", removed)
	}
}

// TestAppendSplicedDigest (task #314): the caller-chosen digest replaces every
// 16-byte 0x0425 resource; a nil digest leaves them untouched.
func TestAppendSplicedDigest(t *testing.T) {
	t.Parallel()
	stale := bytes.Repeat([]byte{0xEE}, DigestSize)
	orig := bytes.Join([][]byte{block(ResourceIPTCDigest, "", stale), block(ResourceIPTC, "", iimOld)}, nil)
	d := Digest(iimNew, 4)
	out := AppendSplicedDigest(nil, orig, iimNew, &d, false)
	if got := FindDigest(out); !bytes.Equal(got, d[:]) {
		t.Errorf("digest = %x, want %x", got, d)
	}
	if got := FindIPTC(out); !bytes.Equal(got, iimNew) {
		t.Errorf("IIM = %x, want %x", got, iimNew)
	}
	keep := AppendSplicedDigest(nil, orig, iimNew, nil, false)
	if got := FindDigest(keep); !bytes.Equal(got, stale) {
		t.Errorf("nil digest changed the 0x0425 resource: %x", got)
	}
}

// TestAppendSplicedDigest_Create (task #323): create appends one 0x0425
// resource only when a non-nil digest is given and the stream holds none.
func TestAppendSplicedDigest_Create(t *testing.T) {
	t.Parallel()
	d := Digest(iimNew, 4)
	orig := block(ResourceIPTC, "", iimOld)
	out := AppendSplicedDigest(nil, orig, iimNew, &d, true)
	if got := FindDigest(out); !bytes.Equal(got, d[:]) {
		t.Errorf("created digest = %x, want %x", got, d)
	}
	if got := FindIPTC(out); !bytes.Equal(got, iimNew) {
		t.Errorf("IIM = %x, want %x", got, iimNew)
	}
	if got := AppendSplicedDigest(nil, orig, iimNew, nil, true); FindDigest(got) != nil {
		t.Error("a nil digest created a 0x0425 resource")
	}
	if got := AppendSplicedDigest(nil, orig, iimNew, &d, false); FindDigest(got) != nil {
		t.Error("create=false created a 0x0425 resource")
	}
}
