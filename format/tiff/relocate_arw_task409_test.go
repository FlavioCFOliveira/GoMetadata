package tiff

// relocate_arw_task409_test.go — task #409: the rebase of the decrypted
// SR2SubIFD of a Sony ARW treats only the SR2DataIFD array (tag 0x74C0) as IFD
// offsets. A LONG array of data whose value falls inside the SR2 block was
// rebased as a pointer, and the "IFD" it addressed was rebased in turn, which
// shifted the pointers of a real sub-IFD twice (ExifTool: "Suspicious
// SR2DataIFD offset for tag 0x7805" on the written Sony A7M3).

import (
	"encoding/binary"
	"testing"
)

func TestRebaseIFDInBlob_R409_OnlyTheDataIFDArrayHoldsIFDs(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	const srcOff, newOff = 1000, 3000
	raw := make([]byte, 200)
	entry := func(at int, tag, typ uint16, cnt, val uint32) {
		le.PutUint16(raw[at:], tag)
		le.PutUint16(raw[at+2:], typ)
		le.PutUint32(raw[at+4:], cnt)
		le.PutUint32(raw[at+8:], val)
	}
	// Root IFD at 0: a data array whose first value equals the address of the
	// first sub-IFD, and the SR2DataIFD array of two sub-IFDs.
	le.PutUint16(raw[0:], 2)
	entry(2, 0x7805, 4, 3, srcOff+64)
	entry(14, 0x74C0, 4, 2, srcOff+76)
	le.PutUint32(raw[64:], srcOff+100) // data that looks like a pointer into the block
	le.PutUint32(raw[68:], 5)
	le.PutUint32(raw[72:], 6)
	le.PutUint32(raw[76:], srcOff+100) // the two sub-IFDs
	le.PutUint32(raw[80:], srcOff+130)
	for i, at := range []int{100, 130} {
		le.PutUint16(raw[at:], 1)
		entry(at+2, 0x7842, 3, 8, uint32(srcOff+160+16*i))
	}

	rebaseIFDInBlob(raw, raw, srcOff, newOff, le, true)

	want := map[int]uint32{
		10:  newOff + 64,  // root: the data array pointer
		22:  newOff + 76,  // root: the SR2DataIFD array pointer
		64:  srcOff + 100, // data: untouched
		68:  5,
		72:  6,
		76:  newOff + 100, // the SR2DataIFD array: IFD offsets
		80:  newOff + 130,
		110: newOff + 160, // sub-IFD 1: rebased once
		140: newOff + 176, // sub-IFD 2: rebased once
	}
	for at, w := range want {
		if got := le.Uint32(raw[at:]); got != w {
			t.Errorf("word at %d = %d, want %d", at, got, w)
		}
	}
}
