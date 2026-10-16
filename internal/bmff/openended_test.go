package bmff

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func box32(size uint32, body ...byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, size)
	b = append(b, "free"...)
	return append(b, body...)
}

func box64(large uint64, body ...byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, 1)
	b = append(b, "free"...)
	b = binary.BigEndian.AppendUint64(b, large)
	return append(b, body...)
}

func TestOpenEndedAndClose(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   []byte
		open bool
		want []byte
	}{
		{"explicit 32-bit", box32(12, 1, 2, 3, 4), false, box32(12, 1, 2, 3, 4)},
		{"size 0", box32(0, 1, 2, 3, 4), true, box32(12, 1, 2, 3, 4)},
		{"size 0 header only", box32(0), true, box32(8)},
		{"largesize explicit", box64(20, 1, 2, 3, 4), false, box64(20, 1, 2, 3, 4)},
		{"largesize 0", box64(0, 1, 2, 3, 4), true, box64(20, 1, 2, 3, 4)},
		{"too short for a header", []byte{0, 0, 0, 0}, false, []byte{0, 0, 0, 0}},
		{"largesize header truncated", []byte{0, 0, 0, 1, 'f', 'r', 'e', 'e'}, false, []byte{0, 0, 0, 1, 'f', 'r', 'e', 'e'}},
	} {
		t.Run("BMFF-OPENENDED-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if got := OpenEnded(tc.in); got != tc.open {
				t.Errorf("OpenEnded = %v, want %v", got, tc.open)
			}
			orig := bytes.Clone(tc.in)
			if got := Close(tc.in); !bytes.Equal(got, tc.want) {
				t.Errorf("Close = %x, want %x", got, tc.want)
			}
			if !bytes.Equal(tc.in, orig) {
				t.Errorf("Close edited its input: %x, was %x", tc.in, orig)
			}
			cp := bytes.Clone(tc.in)
			if ok := CloseInPlace(cp); !ok || !bytes.Equal(cp, tc.want) {
				t.Errorf("CloseInPlace = %v %x, want true %x", ok, cp, tc.want)
			}
		})
	}
}

// TestCloseAbove32Bits: a box too long for the 32-bit size field gets the
// 64-bit header (the limit is lowered so no 4 GiB buffer is needed).
func TestCloseAbove32Bits(t *testing.T) {
	t.Parallel()
	t.Run("BMFF-OPENENDED-01", func(t *testing.T) {
		t.Parallel()
		in := box32(0, 1, 2, 3, 4)
		got := closeWithLimit(in, 8)
		want := box64(20, 1, 2, 3, 4)
		if !bytes.Equal(got, want) {
			t.Errorf("closeWithLimit = %x, want %x", got, want)
		}
		if !OpenEnded(in) || binary.BigEndian.Uint32(in) != 0 {
			t.Error("input edited")
		}
	})
}
