package cr3

import (
	"bytes"
	"errors"
	"testing"
)

// Task #347: Inject returned nil without writing when the file had no moov
// box, or a moov box without the Canon uuid box (the only carrier of the
// CMT1-CMT4 EXIF streams). A write is now either stored — proven by reading
// it back — or refused with ErrNoMetadataContainer and no output.

// task347Case is a CR3 fixture and what Inject must do with it for each kind
// of request.
type task347Case struct {
	name string
	file []byte
	// exifOK / xmpOK: whether a request for EXIF / for XMP is storable.
	exifOK, xmpOK bool
	// moovMissing: a refused request also matches ErrNoMoovBox.
	moovMissing bool
}

func task347Cases() []task347Case {
	ftyp := ftypBox16()
	cmt1 := buildBox("CMT1", minimalTIFF())
	otherUUID := buildUUIDBox([]byte("0123456789abcdef"), cmt1)
	trak := buildBox("trak", buildBox("mdia", nil))
	return []task347Case{
		{name: "no moov", file: ftyp, moovMissing: true},
		{name: "empty file", file: nil, moovMissing: true},
		{name: "moov without uuid box", file: append(append([]byte{}, ftyp...), buildBox("moov", trak)...), xmpOK: true},
		{name: "moov with a non-Canon uuid box", file: append(append([]byte{}, ftyp...), buildBox("moov", otherUUID)...), xmpOK: true},
		{name: "CMT1 directly in moov", file: append(append([]byte{}, ftyp...), buildBox("moov", cmt1)...), xmpOK: true},
		{name: "Canon uuid box", file: buildMinimalCR3(minimalTIFF(), nil), exifOK: true, xmpOK: true},
		{name: "Canon uuid box, 64-bit size", file: append(append([]byte{}, ftyp...),
			buildBox("moov", buildExtendedUUIDBox(canonUUID, cmt1))...), exifOK: true, xmpOK: true},
	}
}

// TestTask347InjectNeverReturnsNilWithoutWriting is the assertion test of #347:
// over every fixture and request, Inject writes the payload (read back by
// Extract) or returns ErrNoMetadataContainer and writes nothing. It never
// returns nil while discarding a requested change. CR3-WRITE-NODROP-01.
func TestTask347InjectNeverReturnsNilWithoutWriting(t *testing.T) {
	t.Parallel()
	xmp := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	// A big-endian stream: it differs from the little-endian EXIF of the
	// fixtures, so a stored write always changes the file.
	newTIFF := []byte{'M', 'M', 0, 0x2A, 0, 0, 0, 8, 0, 0, 0, 0, 0, 0}
	for _, tc := range task347Cases() {
		for _, req := range []struct {
			name      string
			exif, xmp []byte
		}{
			{"EXIF and XMP", newTIFF, xmp},
			{"EXIF only", newTIFF, nil},
			{"XMP only", nil, xmp},
		} {
			t.Run("CR3-WRITE-NODROP-01/"+tc.name+"/"+req.name, func(t *testing.T) {
				t.Parallel()
				storable := (req.exif == nil || tc.exifOK) && (req.xmp == nil || tc.xmpOK)
				var out bytes.Buffer
				err := Inject(bytes.NewReader(tc.file), &out, req.exif, nil, req.xmp, true)
				if !storable {
					if !errors.Is(err, ErrNoMetadataContainer) {
						t.Fatalf("Inject error = %v, want ErrNoMetadataContainer", err)
					}
					if tc.moovMissing && !errors.Is(err, ErrNoMoovBox) {
						t.Errorf("Inject error = %v, want it to match ErrNoMoovBox too", err)
					}
					if out.Len() != 0 {
						t.Errorf("a refused write produced %d bytes of output", out.Len())
					}
					return
				}
				if err != nil {
					t.Fatalf("Inject: %v", err)
				}
				if bytes.Equal(out.Bytes(), tc.file) {
					t.Fatal("Inject returned nil but left the file unchanged")
				}
				gotEXIF, _, gotXMP, _ := Extract(bytes.NewReader(out.Bytes()))
				if req.exif != nil && !bytes.Equal(gotEXIF, req.exif) {
					t.Errorf("EXIF read back = %x, want %x", gotEXIF, req.exif)
				}
				if req.xmp != nil && !bytes.Equal(gotXMP, req.xmp) {
					t.Errorf("XMP read back = %q, want %q", gotXMP, req.xmp)
				}
			})
		}
	}
}

// TestTask347NothingRequestedIsVerbatim: with no storable payload (none, or
// only IPTC, which CR3 cannot carry) the file is copied byte for byte,
// whatever its structure. CR3-WRITE-NODROP-01.
func TestTask347NothingRequestedIsVerbatim(t *testing.T) {
	t.Parallel()
	for _, tc := range task347Cases() {
		t.Run("CR3-WRITE-NODROP-01/verbatim/"+tc.name, func(t *testing.T) {
			t.Parallel()
			for _, iptc := range [][]byte{nil, []byte("iptc")} {
				var out bytes.Buffer
				if err := Inject(bytes.NewReader(tc.file), &out, nil, iptc, nil, true); err != nil {
					t.Fatalf("Inject: %v", err)
				}
				if !bytes.Equal(out.Bytes(), tc.file) {
					t.Error("pass-through changed the file")
				}
			}
		})
	}
}

// TestTask347OpenEndedChildKeepsAppendedCMT1 is the regression gate for the
// defect FuzzCR3Inject found while closing #347: a Canon uuid box whose last
// child has a size field of 0 ("extends to the end of the container", ISO/IEC
// 14496-12 §4.2) swallowed the CMT1 box Inject appended after it, so the
// written EXIF was unreadable although Inject returned nil. CR3-WRITE-NODROP-01.
func TestTask347OpenEndedChildKeepsAppendedCMT1(t *testing.T) {
	t.Parallel()
	// A child 'free' box with size 0 and 4 payload bytes; no CMT1.
	openEnded := []byte{0, 0, 0, 0, 'f', 'r', 'e', 'e', 1, 2, 3, 4}
	file := append(append([]byte{}, ftypBox16()...), buildBox("moov", buildUUIDBox(canonUUID, openEnded))...)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(file), &out, minimalTIFF(), nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	gotEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil || !bytes.Equal(gotEXIF, minimalTIFF()) {
		t.Fatalf("EXIF read back = %x, %v, want %x", gotEXIF, err, minimalTIFF())
	}
	if !bytes.Contains(out.Bytes(), []byte{1, 2, 3, 4}) {
		t.Error("the open-ended child's payload was dropped")
	}
}
