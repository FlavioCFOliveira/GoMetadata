package gometadata

// makernote_srw_task408_test.go — task #408: writing EXIF to a Samsung SRW keeps
// the MakerNote: its tags and values, and the PreviewIFD and preview JPEG that
// follow its declared end.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestWriteSRW_R408_KeepsMakerNote: after SetCaption and Write, the MakerNote of
// each SRW of the corpus reads as the source's does, and the preview image that
// ExifTool 13.55 reports (length and SHA-256 captured once with "exiftool -b
// -PreviewImage"; no test runs it) is in the output byte for byte. The IFD0 of
// the NX3000 holds an all-zero padding entry, which sorted first made ExifTool
// read none of the file.
func TestWriteSRW_R408_KeepsMakerNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file    string
		preview int
		sha     string
	}{
		{"Samsung NX3000.srw", 795708, "a2f50380b02df760f0f05e34156ee807c066354a057f846a641c3b6ca840e616"},
		{"Samsung NX1.srw", 723727, "6f3023e710eaddba3cbef118fed9ecfd6d45be3b2e9219da8645731e1f6221f2"},
		{"Samsung NX20.srw", 157552, "f5c0040820bb520fe9c1ac7a7bdff74401cc985be2d28f430c9923f71f41bbf6"},
		{"Samsung EK-GN120 (Galaxy NX).srw", 228820, "0461cb6aff3471d6caf649bdf5cd7bc66a1cfa1da14b5ea7e376f57a89630754"},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			data := mneCorpus(t, "raw/metadata-extractor/"+tc.file)
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			want := mneTags(t, m)
			m.SetCaption("x")
			var out bytes.Buffer
			if err := Write(bytes.NewReader(data), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Read of the output: %v", err)
			}
			if m2.EXIF == nil || m2.EXIF.IFD0 == nil || len(m2.EXIF.IFD0.Entries) == 0 || m2.EXIF.IFD0.Entries[0].Type == 0 {
				t.Fatal("IFD0 of the output opens with an entry of type 0, which readers take for a corrupted IFD")
			}
			got := mneTags(t, m2)
			if len(got) != len(want) {
				t.Errorf("%d MakerNote tags after the write, %d before", len(got), len(want))
			}
			for tag, w := range want {
				if got[tag] != w {
					t.Errorf("MakerNote tag %#04x changed by the write", uint16(tag))
				}
			}
			// The preview follows the declared end of the MakerNote in the source:
			// it starts with an SOI marker and has the length ExifTool reports.
			found := false
			for i := 0; !found; {
				j := bytes.Index(out.Bytes()[i:], []byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x84})
				if j < 0 {
					break
				}
				i += j
				if i+tc.preview <= out.Len() {
					sum := sha256.Sum256(out.Bytes()[i : i+tc.preview])
					found = hex.EncodeToString(sum[:]) == tc.sha
				}
				i++
			}
			if !found {
				t.Error("the preview image is not in the output")
			}
		})
	}
}
