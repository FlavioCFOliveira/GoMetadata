package webp

import (
	"bytes"
	"encoding/binary"
)

// writeRIFFChunk appends a RIFF chunk (header, data and the pad byte of an odd
// size) to w. Test fixtures are built with it.
func writeRIFFChunk(w *bytes.Buffer, id [4]byte, data []byte) {
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: test fixture sizes are small
	_, _ = w.Write(id[:])                                   // a bytes.Buffer write never fails
	_, _ = w.Write(sz[:])
	_, _ = w.Write(data)
	if len(data)%2 != 0 {
		_ = w.WriteByte(0x00)
	}
}
