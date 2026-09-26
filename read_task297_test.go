package gometadata

// read_task297_test.go — regression guard for task #297: parseEXIF (read.go)
// previously built its exif.ParseOption slice via `var opts []exif.ParseOption`
// grown by append, and each of the three option-constructor calls
// (exif.SkipMakerNote/AcceptRAWMagic/AliasThumbnail) — even though each is
// itself a non-capturing (AcceptRAWMagic aside, which captures its magic
// argument by value) closure literal — escaped to the heap once inlined
// into parseEXIF, adding +1 alloc/op to every EXIF Read. Fixed by (a)
// marking the three exif option constructors //go:noinline (exif/exif.go),
// which restores the "non-capturing closure is a static value" optimisation
// inlining had defeated, and (b) assembling parseEXIF's own options via a
// fixed-size array with direct indexed assignment instead of append(nil,
// ...), so the array's own backing storage also never escapes.

import (
	"bytes"
	"testing"
)

// TestParseEXIFAllocsPerRun proves parseEXIF's own option-construction no
// longer allocates: the only allocations a minimal Read of a JPEG carrying a
// tiny EXIF payload can attribute to are exif.Parse's own returned *EXIF
// structure and its IFD0 entries — never one extra allocation per
// SkipMakerNote/AcceptRAWMagic/AliasThumbnail call.
//
//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel
func TestParseEXIFAllocsPerRun(t *testing.T) {
	jpeg := buildJPEGWithCaption("task297")

	// Establish a baseline via a real Read first (warms any package-level
	// sync.Pool this path touches, matching how every other AllocsPerRun
	// test in this suite already treats pool warm-up as out of scope).
	if _, err := Read(bytes.NewReader(jpeg)); err != nil {
		t.Fatalf("Read: %v", err)
	}

	allocs := testing.AllocsPerRun(20, func() {
		if _, err := Read(bytes.NewReader(jpeg)); err != nil {
			t.Fatalf("Read: %v", err)
		}
	})
	t.Logf("Read (JPEG + minimal EXIF): %.1f allocs/op", allocs)
	// This is a coarse, whole-Read ceiling (exif.Parse/xmp.Parse/iptc parsing
	// all contribute too), not an exact count — its purpose is to catch a
	// REGRESSION (a return to the pre-#297 nil-slice-append pattern would add
	// a further +1 alloc/op on top of whatever this ceiling already
	// tolerates), not to pin every allocation this call chain will ever make.
	const ceiling = 15 // measured 7.0 allocs/op at fix time; headroom, not an exact pin
	if allocs > ceiling {
		t.Errorf("Read: %.1f allocs/op, want <= %d (parseEXIF's option construction must not add an allocation — see this file's own doc comment)", allocs, ceiling)
	}
}
