package tiff

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
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

	// Seed 5: BigTIFF LE header (magic 0x002B) — task #54: BigTIFF is now supported.
	// BigTIFF spec §2: magic 0x002B, 16-byte header with 8-byte IFD offset.
	// This seed exercises the BigTIFF parse path; IFD0 offset = 16 (past buffer,
	// so IFD scan returns zero entries — but rawEXIF is non-nil and no panic).
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

	// Seed 12: TIFF LE with IFD0 offset == 0x80000000 (task #74 regression seed).
	// On a 32-bit build, int(0x80000000) == -2147483648; the uint64 bounds guard
	// must fire and return an error rather than allowing a slice-OOB panic.
	{
		buf := make([]byte, 8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002A)
		binary.LittleEndian.PutUint32(buf[4:], 0x80000000)
		f.Add(buf)
	}

	// Seed 13: TIFF BE with IFD0 offset == 0xFFFFFFFF (max uint32, task #74).
	{
		buf := make([]byte, 8)
		buf[0], buf[1] = 'M', 'M'
		binary.BigEndian.PutUint16(buf[2:], 0x002A)
		binary.BigEndian.PutUint32(buf[4:], 0xFFFFFFFF)
		f.Add(buf)
	}

	// Seed 14: BigTIFF BE (task #54) — exercises the BE BigTIFF code path.
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'M', 'M'
		binary.BigEndian.PutUint16(buf[2:], 0x002B)
		binary.BigEndian.PutUint16(buf[4:], 8)
		binary.BigEndian.PutUint16(buf[6:], 0)
		binary.BigEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed 15: BigTIFF LE with bad offset-bytesize (not 8) — must return error.
	// BigTIFF spec §2: bytesize-of-offsets must be 8; any other value is invalid.
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 4) // invalid: not 8
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed 16: BigTIFF LE with huge IFD entry count (near MaxUint64).
	// The DoS guard must clamp count before iterating.
	{
		buf := make([]byte, 32)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], 16)          // IFD0 at 16
		binary.LittleEndian.PutUint64(buf[16:], ^uint64(0)) // count = MaxUint64
		f.Add(buf)
	}

	// Seed 17: BigTIFF LE with IPTC entry having count that would overflow uint64
	// (count * typeSize overflows). The overflow guard must skip the entry.
	{
		buf := make([]byte, 52)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], 16)
		binary.LittleEndian.PutUint64(buf[16:], 1) // 1 entry
		// IPTC tag, RATIONAL (sz=8), count = MaxUint64/8+1 → overflow.
		binary.LittleEndian.PutUint16(buf[24:], 0x83BB)
		binary.LittleEndian.PutUint16(buf[26:], 5) // RATIONAL
		binary.LittleEndian.PutUint64(buf[28:], ^uint64(0)/8+1)
		binary.LittleEndian.PutUint64(buf[36:], 0)
		f.Add(buf)
	}

	// Seed 18: BigTIFF LE with IFD0 offset pointing past EOF.
	// extractBigTIFF must return rawEXIF without panicking.
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], ^uint64(0)) // offset = MaxUint64
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzTIFFInject feeds arbitrary bytes as the source TIFF container and asserts
// that Inject never panics — it must return an error or write valid output,
// never crash.
//
// The TIFF Inject path calls exif.Parse (and therefore the IFD traversal cycle
// guard and all field-width arithmetic) whenever rawIPTC or rawXMP is non-nil.
// Seeds cover the key structural variations that stress the write path:
//   - valid LE/BE TIFF headers so Inject can reach the exif.Parse call
//   - cyclic IFD chains (exercises cycle-detection in exif.traverse)
//   - truncated inputs (exercises early-return error paths)
//   - BigTIFF (exercises the container-width-aware relocation path added in
//     task #270 — tiff.Inject fully supports BigTIFF sources, it does not
//     reject them)
//
// Note: prior to task #271, the top-level gometadata.Write gated BigTIFF
// sources behind ErrWriteNotSupported even though tiff.Inject itself never
// returned that error — it always processed the call and either succeeded
// or returned a parse/encode error. That root-package gate has since been
// removed (task #271); this fuzz target continues to exercise tiff.Inject
// directly regardless, since it is the lower-level entry point.
func FuzzTIFFInject(f *testing.F) {
	// Seed 1: minimal LE TIFF with 0 entries — Inject with IPTC/XMP must call
	// exif.Parse and succeed (empty IFD0, no cycle, valid header).
	minLE := buildMinimalTIFF(binary.LittleEndian,
		[]byte("fuzz-iptc-seed"),
		[]byte("<xmpmeta/>"),
	)
	f.Add(minLE)

	// Seed 2: minimal BE TIFF — exercises big-endian parsing in exif.Parse.
	minBE := buildMinimalTIFF(binary.BigEndian,
		[]byte("fuzz-iptc-be"),
		[]byte("<xmpmeta be='1'/>"),
	)
	f.Add(minBE)

	// Seed 3: empty input — exercises ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 4: truncated header — 4 bytes (valid byte-order mark, no magic/offset).
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	// Seed 5: cyclic IFD chain — exercises the cycle-detection guard in
	// exif.traverse when Inject triggers exif.Parse.
	f.Add(buildCyclicIFDTIFF())

	// Seed 6: multi-page TIFF (IFD chain) — exercises IFD traversal with IPTC
	// in IFD0 and XMP in IFD1 (IFD1 payload is not extracted by tiff.Extract).
	f.Add(buildMultiPageTIFF(
		[]byte("iptc-fuzz-inject-seed"),
		[]byte("<xmpmeta inject='1'/>"),
	))

	// Seed 7: BigTIFF LE, empty IFD0 — exercises the BigTIFF parse path in
	// Extract (task #54) and the BigTIFF copy-and-relocate path in Inject
	// (task #270: standalone BigTIFF container write). This minimal header
	// has no entries; Inject must handle the resulting empty-IFD0 relocation
	// (or a parse error on the zero-length tail) without panicking.
	bigTIFFLE := make([]byte, 16)
	bigTIFFLE[0], bigTIFFLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFFLE[2:], 0x002B) // BigTIFF magic
	binary.LittleEndian.PutUint16(bigTIFFLE[4:], 8)
	binary.LittleEndian.PutUint16(bigTIFFLE[6:], 0)
	binary.LittleEndian.PutUint64(bigTIFFLE[8:], 16)
	f.Add(bigTIFFLE)

	// Seed 7b/7c/7d (task #270): structurally complete BigTIFF fixtures with
	// real IFD0 content, giving the fuzzer real starting points that exercise
	// relocateTIFFFromParsed's BigTIFF-aware code paths (readIFD0Offset,
	// findEntryInIFD, extractParallelOffsetBlocks with LONG8 elements,
	// enumerateSubIFDs/patchSubIFDPointers for all four legitimate 0x014A
	// type codes) rather than only the classic-TIFF paths every other seed
	// above covers. buildSyntheticBigTIFF is shared with
	// conformance_bigtiff_write_test.go.
	f.Add(buildSyntheticBigTIFF(binary.LittleEndian, uint16(exif.TypeLong8), 0))
	f.Add(buildSyntheticBigTIFF(binary.BigEndian, uint16(exif.TypeLong), 13)) // 0x014A as IFD (EXIF-3.0/TIFF-Extension type-13 collision case)
	f.Add(buildSyntheticBigTIFF(binary.LittleEndian, uint16(exif.TypeLong), uint16(exif.TypeIFD8)))

	// Seed 8: TIFF with max-value count entry — exercises overflow guards in
	// exif.Parse field-width arithmetic.
	{
		buf := make([]byte, 26)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002A)
		binary.LittleEndian.PutUint32(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[8:], 1)
		binary.LittleEndian.PutUint16(buf[10:], 0x83BB) // IPTC tag
		binary.LittleEndian.PutUint16(buf[12:], 7)      // UNDEFINED
		binary.LittleEndian.PutUint32(buf[14:], 0xFFFFFFFF)
		binary.LittleEndian.PutUint32(buf[18:], 0xFFFFFFFF)
		f.Add(buf)
	}

	// Seed 9: valid LE TIFF with IFD0 offset == 0x80000000 — exercises the
	// uint64 bounds guard in the IFD-offset reader (task #74 regression seed).
	{
		buf := make([]byte, 8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002A)
		binary.LittleEndian.PutUint32(buf[4:], 0x80000000)
		f.Add(buf)
	}

	// Fixed metadata payloads used for all fuzz iterations. The fuzzer varies
	// the container bytes; the IPTC/XMP payloads are kept short and constant
	// so that Inject reaches exif.Parse on every iteration.
	rawIPTC := []byte("fuzz-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		//
		// Pass data as both the reader (source container) and as rawEXIF so that
		// Inject uses the fuzz-controlled bytes as the base TIFF to parse.
		// When rawIPTC and rawXMP are non-nil, Inject calls exif.Parse on the
		// base, which exercises the full IFD traversal, cycle detection, and
		// field-width arithmetic.
		err := Inject(bytes.NewReader(data), io.Discard, data, rawIPTC, rawXMP, true)
		_ = err
	})
}
