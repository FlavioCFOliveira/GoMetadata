package heif

import (
	"bytes"
	"testing"
)

// Task #411.
//
// ISO/IEC 14496-12 §8.11.3.3 locates item data at base_offset + extent_offset.
// Inject used to write every rewritten item with base_offset 0 and the whole
// position in extent_offset. Exiv2 0.28 (BmffImage::boxHandler, TAG_iloc)
// reads only base_offset when an item is 18 bytes long (iloc version 0, 4-byte
// offset, length and base_offset), so the Exif item of such a file was
// invisible to it although the file was valid. Inject now stores the position
// in base_offset, with extent_offset 0, whenever the source declares a
// base_offset field; without one, in extent_offset as before.

// TestTask411RewrittenItemsUseBaseOffset checks the iloc split of the items
// Inject writes, and that the payloads still resolve to the new data.
func TestTask411RewrittenItemsUseBaseOffset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		layout         task334Layout
		wantBaseOffset bool
	}{
		{"base_offset field 4", task334Layout{withItems: true, offsetSize: 4, lengthSize: 4, baseOffsetSize: 4}, true},
		{"base_offset field 8", task334Layout{withItems: true, offsetSize: 4, lengthSize: 4, baseOffsetSize: 8}, true},
		{"no base_offset field", task334Layout{withItems: true, offsetSize: 4, lengthSize: 4}, false},
	} {
		t.Run("HEIF-ILOC-BASE-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask334File(tc.layout)
			newEXIF := conformanceMinimalTIFF()
			newXMP := []byte("<new/>")
			out := injectBytes(t, src, newEXIF, newXMP)

			meta, err := findMetaContent(out)
			if err != nil {
				t.Fatal(err)
			}
			info, ok := parseIlocFull(meta)
			if !ok {
				t.Fatal("iloc of the output does not parse")
			}
			seen := 0
			for _, it := range info.items {
				if it.id != 2 && it.id != 3 {
					continue
				}
				seen++
				if len(it.extents) != 1 {
					t.Fatalf("item %d has %d extents, want 1", it.id, len(it.extents))
				}
				if tc.wantBaseOffset {
					if it.baseOffset == 0 || it.extents[0].offset != 0 {
						t.Errorf("item %d: base_offset %d, extent_offset %d; want the position in base_offset and extent_offset 0",
							it.id, it.baseOffset, it.extents[0].offset)
					}
				} else if it.baseOffset != 0 || it.extents[0].offset == 0 {
					t.Errorf("item %d: base_offset %d, extent_offset %d; want the position in extent_offset",
						it.id, it.baseOffset, it.extents[0].offset)
				}
			}
			if seen != 2 {
				t.Fatalf("found %d Exif/XMP items, want 2", seen)
			}
			// The Exif item holds the 4-byte exif_tiff_header_offset (0) followed
			// by the TIFF data (ISO/IEC 23008-12 Annex A.2).
			if got, want := itemBytes(t, out, 2), append([]byte{0, 0, 0, 0}, newEXIF...); !bytes.Equal(got, want) {
				t.Errorf("Exif item = %x, want %x", got, want)
			}
			if got := itemBytes(t, out, 3); !bytes.Equal(got, newXMP) {
				t.Errorf("XMP item = %q, want %q", got, newXMP)
			}
		})
	}
}
