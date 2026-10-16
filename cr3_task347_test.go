package gometadata

// cr3_task347_test.go — task #347: Write to a CR3 whose moov box has no Canon
// uuid box (the only carrier of the EXIF streams) is refused with a specific
// error and writes nothing, instead of returning nil and dropping the change.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
)

// cr3WithoutCanonBox returns src (a CR3) with the Canon uuid box (UUID
// 85c0b687-820f-11e0-8111-f4ce462b6a48) removed from its moov box and the
// size of moov adjusted.
func cr3WithoutCanonBox(t *testing.T, src []byte) []byte {
	t.Helper()
	canon := []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}
	for pos := 0; pos+8 <= len(src); {
		size, hl := uint64(binary.BigEndian.Uint32(src[pos:])), 8
		if size == 1 {
			size, hl = binary.BigEndian.Uint64(src[pos+8:]), 16
		}
		if size == 0 {
			size = uint64(len(src) - pos)
		}
		end := pos + int(size) //nolint:gosec // G115: bounded by the test file size
		if string(src[pos+4:pos+8]) != "moov" {
			pos = end
			continue
		}
		for c := pos + hl; c+8 <= end; {
			csize := int(binary.BigEndian.Uint32(src[c:]))
			if csize < 8 || c+csize > end {
				break
			}
			if string(src[c+4:c+8]) == "uuid" && bytes.Equal(src[c+8:c+24], canon) {
				out := append(bytes.Clone(src[:c]), src[c+csize:]...)
				if hl == 8 {
					binary.BigEndian.PutUint32(out[pos:], uint32(int(size)-csize)) //nolint:gosec // G115: bounded by the test file size
				} else {
					binary.BigEndian.PutUint64(out[pos+8:], size-uint64(csize))
				}
				return out
			}
			c += csize
		}
		t.Fatal("no Canon uuid box in moov")
	}
	t.Fatal("no moov box")
	return nil
}

// TestTask347CR3WriteWithoutCanonBoxIsRefused: Read, SetCaption and Write on a
// CR3 without the Canon uuid box return an error that names the cause and
// leave the output empty. CR3-WRITE-NODROP-01.
func TestTask347CR3WriteWithoutCanonBoxIsRefused(t *testing.T) {
	t.Parallel()
	src := cr3WithoutCanonBox(t, readCR3Corpus(t, "exiftool/CanonRaw.cr3"))
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("c")
	var out bytes.Buffer
	err = Write(bytes.NewReader(src), &out, m)
	if !errors.Is(err, cr3.ErrNoMetadataContainer) {
		t.Fatalf("Write error = %v, want cr3.ErrNoMetadataContainer", err)
	}
	if out.Len() != 0 {
		t.Errorf("a refused write produced %d bytes", out.Len())
	}
}
