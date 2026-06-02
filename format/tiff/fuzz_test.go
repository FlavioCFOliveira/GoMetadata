package tiff

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzTIFFExtract(f *testing.F) {
	// Seed 1: minimal little-endian TIFF with 0-entry IFD0.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed 2: minimal big-endian TIFF with 0-entry IFD0.
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	binary.BigEndian.PutUint16(minBE[2:], 0x002A)
	binary.BigEndian.PutUint32(minBE[4:], 8)
	f.Add(minBE)

	// Seed 3: empty input.
	f.Add([]byte{})

	// Seed 4: truncated header (4 bytes — valid byte-order mark, no IFD offset).
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	// Seed 5: BigTIFF LE header (magic 0x002B).
	// BigTIFF spec §2: magic 0x002B, 16-byte header with 8-byte IFD offset.
	// The library does not support BigTIFF; this seed exercises the graceful
	// error path without panicking.
	bigTIFFLE := make([]byte, 16)
	bigTIFFLE[0], bigTIFFLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFFLE[2:], 0x002B)
	binary.LittleEndian.PutUint16(bigTIFFLE[4:], 8)
	binary.LittleEndian.PutUint16(bigTIFFLE[6:], 0)
	binary.LittleEndian.PutUint64(bigTIFFLE[8:], 16)
	f.Add(bigTIFFLE)

	// Seed 6: TIFF truncated mid-IFD.
	// Header valid; IFD0 entry count = 5 but buffer ends after 2 bytes of entries.
	// TIFF 6.0 §2: each entry is 12 bytes — a truncated entry list is malformed.
	truncMidIFD := make([]byte, 22) // 8 (hdr) + 2 (count) + 12 (partial entry)
	truncMidIFD[0], truncMidIFD[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(truncMidIFD[2:], 0x002A)
	binary.LittleEndian.PutUint32(truncMidIFD[4:], 8)
	binary.LittleEndian.PutUint16(truncMidIFD[8:], 5) // claims 5 entries, only 12 bytes follow
	f.Add(truncMidIFD)

	// Seed 7: TIFF truncated mid-value.
	// IFD entry claims 100-byte out-of-line value at offset 50, but buffer is only 26 bytes.
	truncMidVal := make([]byte, 26)
	truncMidVal[0], truncMidVal[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(truncMidVal[2:], 0x002A)
	binary.LittleEndian.PutUint32(truncMidVal[4:], 8)
	binary.LittleEndian.PutUint16(truncMidVal[8:], 1) // 1 entry
	binary.LittleEndian.PutUint16(truncMidVal[10:], 0x83BB)
	binary.LittleEndian.PutUint16(truncMidVal[12:], 7)
	binary.LittleEndian.PutUint32(truncMidVal[14:], 100)
	binary.LittleEndian.PutUint32(truncMidVal[18:], 50) // offset past end of buf
	f.Add(truncMidVal)

	// Seed 8: multi-page TIFF (IFD0 → IFD1 → end).
	// Exercises the IFD chain path (extractTagValues reads only IFD0).
	f.Add(buildMultiPageTIFF(
		[]byte("iptc-fuzz-seed-long-enough"),
		[]byte("<xmpmeta/>"),
	))

	// Seed 9: TIFF with cyclic IFD chain (IFD0 next-ptr → IFD0).
	f.Add(buildCyclicIFDTIFF())

	// Seed 10: TIFF with IPTC and XMP tags in IFD0 (LE).
	f.Add(buildMinimalTIFF(binary.LittleEndian,
		[]byte("iptc-fuzz-seed"),
		[]byte("<xmpmeta/>"),
	))

	// Seed 11: TIFF with max-value count entry (overflow probe).
	{
		buf := make([]byte, 26)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002A)
		binary.LittleEndian.PutUint32(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[8:], 1)
		binary.LittleEndian.PutUint16(buf[10:], 0x83BB)
		binary.LittleEndian.PutUint16(buf[12:], 7)
		binary.LittleEndian.PutUint32(buf[14:], 0xFFFFFFFF) // max count
		binary.LittleEndian.PutUint32(buf[18:], 0xFFFFFFFF) // huge offset
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
