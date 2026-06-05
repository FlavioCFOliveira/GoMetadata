package gometadata

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/format/png"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/arw"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr2"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/dng"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/nef"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/orf"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/rw2"
	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/format/webp"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// injectors maps each FormatID to its Inject function.
//
// The preserveUnknownSegments bool is the last parameter: when false, each
// injector either drops non-essential segments (JPEG) or returns
// ErrPreserveUnknownSegmentsNotSupported (PNG, WebP, HEIF/AVIF, CR3).
// TIFF-based formats are dispatched to dedicated write functions in Write()
// before injectByFormat is reached; the entries below are never invoked by
// the top-level Write path for those formats.
//
//nolint:gochecknoglobals // dispatch table: read-only after init, never mutated
var injectors = map[format.FormatID]func(io.ReadSeeker, io.Writer, []byte, []byte, []byte, bool) error{
	format.FormatJPEG: jpeg.Inject,
	format.FormatTIFF: tiff.Inject,
	format.FormatPNG:  png.Inject,
	format.FormatWebP: webp.Inject,
	// AVIF uses the same ISOBMFF container as HEIF; delegate to the HEIF handler.
	format.FormatHEIF: heif.Inject,
	format.FormatAVIF: heif.Inject,
	format.FormatCR2:  cr2.Inject,
	format.FormatCR3:  cr3.Inject,
	format.FormatNEF:  nef.Inject,
	format.FormatARW:  arw.Inject,
	format.FormatDNG:  dng.Inject,
	format.FormatORF:  orf.Inject,
	format.FormatRW2:  rw2.Inject,
}

// Write reads the image from r, applies the metadata in m, and writes the
// result to w. Image data and unmodified metadata segments are preserved
// byte-for-byte. r must support seeking (io.ReadSeeker).
//
// Write calls m.Validate before performing any I/O. A non-nil error from
// Validate is returned unchanged so callers can inspect it with errors.Is.
func Write(r io.ReadSeeker, w io.Writer, m *Metadata, opts ...WriteOption) error { //nolint:cyclop,gocyclo // format dispatch requires per-format branches; adding NEF (#102) incremented complexity by 1; splitting would reduce clarity
	// Validate structural consistency before any I/O. This covers nil IFD0,
	// nil XMP.Properties, and unknown format, replacing the previous inline guard.
	if err := m.Validate(); err != nil {
		return err
	}

	cfg := &writeConfig{preserveUnknownSegments: true}
	for _, o := range opts {
		o(cfg)
	}

	// Detect container format.
	fmtID, err := format.Detect(r)
	if err != nil {
		return fmt.Errorf("gometadata: format detection: %w", err)
	}
	if fmtID == format.FormatUnknown {
		return &UnsupportedFormatError{}
	}

	// All TIFF-based containers use dedicated write paths that keep the ORIGINAL
	// TIFF bytes as the image-data relocation base (epic #33 Option A).  Passing
	// an exif.Encode-produced skeleton as the relocation base causes
	// ErrBlockOutOfBounds because the skeleton carries no image blocks (task #97).
	//
	// FormatTIFF, FormatDNG, FormatCR2: standard copy-and-relocate (writeTIFF).
	// FormatNEF: Nikon MakerNote blob extension + PreviewIFD relocation (writeTIFFNEF, task #102).
	// FormatARW: Sony MakerNote TIFF-absolute rebase + SR2Private block (writeTIFFARW, task #103).
	// FormatORF: non-standard IIRO/IIRS magic patch-and-restore (writeTIFFORF, task #104).
	// FormatRW2: Panasonic GUID insertion + IFD0 offset rebasing (writeTIFFRW2, task #104).
	if fmtID == format.FormatTIFF || fmtID == format.FormatDNG ||
		fmtID == format.FormatCR2 {
		return writeTIFF(r, w, m)
	}

	// FormatNEF uses the NEF-specific write path that extends the Nikon MakerNote
	// blob to cover PreviewIFD/NikonScanIFD, enumerates the PreviewIFD image block,
	// and patches the MakerNote-relative PreviewIFD offsets after relocation.
	// Un-gated in task #102 after real-corpus validation.
	if fmtID == format.FormatNEF {
		return writeTIFFNEF(r, w, m)
	}

	// FormatARW uses the ARW-specific write path that:
	//   - Rebases all Sony MakerNote OOL offsets (Sony uses TIFF-absolute offsets,
	//     not blob-relative like Canon, so the 52 MakerNote tags are otherwise lost).
	//   - Extracts the SR2Private (0xC634) block (37 kB encrypted blob + IDC_IFD),
	//     appends it verbatim, rebases its internal TIFF-absolute pointers, and
	//     patches the IFD0 tag to point to the new SR2 block position.
	// Un-gated in task #103 after real-corpus validation (Sony DSLR-A500.arw).
	if fmtID == format.FormatARW {
		return writeTIFFARW(r, w, m)
	}

	// FormatORF uses the ORF-specific write path that patches the non-standard
	// Olympus magic bytes (IIRO or IIRS at bytes [2:4]) to standard TIFF magic
	// before copy-and-relocate, and restores the original magic variant in the
	// output.  Both IIRO (Olympus DSLRs, E-series, OM-D) and IIRS (older compacts)
	// are supported.
	// Un-gated in task #104 after real-corpus validation.
	if fmtID == format.FormatORF {
		return writeTIFFORF(r, w, m)
	}

	// FormatRW2 uses the RW2-specific write path that:
	//   - Saves the 16-byte Panasonic device GUID at bytes [8:24].
	//   - Patches the non-standard "IIU\x00" magic to standard TIFF magic.
	//   - After copy-and-relocate: inserts the GUID back at position 8, updates the
	//     IFD0 offset in the header from 8 to 24, and rebases all absolute IFD0 OOL
	//     pointers by +16 for the GUID insertion.
	//   - RawDataOffset (0x0118) is registered as a standalone imageBlock; its
	//     inline val_or_off is patched with the new raw sensor data offset.
	// Un-gated in task #104 after real-corpus validation.
	if fmtID == format.FormatRW2 {
		return writeTIFFRW2(r, w, m)
	}

	rawEXIF, rawIPTC, rawXMP, err := encodeMetadata(m, fmtID)
	if err != nil {
		return err
	}

	return injectByFormat(r, w, fmtID, rawEXIF, rawIPTC, rawXMP, cfg.preserveUnknownSegments)
}

// WriteFile reads the image at path, applies the metadata in m, and writes
// the result back to the same file atomically. It is a convenience wrapper
// around Write.
//
// The temporary file is created in the same directory as path so that the
// final os.Rename is always an intra-filesystem operation. This prevents
// EXDEV errors that occur when the system's default temp directory ($TMPDIR)
// lives on a different filesystem (e.g., in containers or NAS mounts).
func WriteFile(path string, m *Metadata, opts ...WriteOption) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("gometadata: open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("gometadata: stat file: %w", err)
	}

	// Place the temp file in the same directory as path so the eventual rename
	// is guaranteed to be atomic (same filesystem, no EXDEV).
	tmp, err := os.CreateTemp(filepath.Dir(path), "gometadata-*")
	if err != nil {
		return fmt.Errorf("gometadata: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// renamed tracks whether os.Rename has successfully moved tmpName to path.
	// The deferred cleanup removes the temp file only when it is still on disk
	// (i.e., rename has not yet occurred or has failed). os.Remove on a
	// non-existent path returns an error that we silently ignore, making this
	// safe to call unconditionally.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	// Preserve original file permissions before writing any data.
	if err := tmp.Chmod(fi.Mode()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gometadata: chmod temp file: %w", err)
	}

	if err := Write(f, tmp, m, opts...); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gometadata: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("gometadata: rename temp file: %w", err)
	}
	renamed = true
	return nil
}

// writeTIFF handles metadata injection for standard-magic TIFF-based
// containers: FormatTIFF, FormatDNG, and FormatCR2.
//
// The standard encodeMetadata/injectByFormat pipeline is not safe for
// TIFF-based containers because encodeEXIF calls exif.Encode which produces
// an IFD skeleton without image blocks. Passing that skeleton as the
// relocation base causes ErrBlockOutOfBounds (task #97).
//
// writeTIFF separates the two concerns:
//   - originalBytes: the ORIGINAL TIFF bytes, read from r (or from
//     m.rawEXIF which tiff.Extract already populated). Used as the image-data
//     source in tiff.InjectWithEXIF → relocateTIFFFromParsed step 12.
//   - modifiedEXIF: m.EXIF as-is (already mutated by Set* calls). Its IFDs
//     carry both the edited metadata AND the original image-block offsets
//     (StripOffsets/TileOffsets still point at originalBytes positions).
//
// IPTC and XMP are encoded the normal way and upserted into IFD0 by
// tiff.InjectWithEXIF → relocateTIFFFromParsed step 2.
//
// CR2 uses standard LE TIFF magic (II*\0) and parses via exif.Parse.
// MakerNote blob is copied verbatim — per SPIKE #24, Canon MakerNotes use
// blob-relative (self-relative) offsets, so verbatim copying is safe.
//
// DNG write is enabled (bug #98 fixed). The SubIFD relocation path now
// preserves ALL out-of-line value areas in each SubIFD (RATIONAL XResolution,
// YResolution, etc.) by updating their valOrOff pointers to the new absolute
// positions. The fix is in patchRawIFDOffsets (format/tiff/relocate.go).
func writeTIFF(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic for originalBytes source + nil guards + three encode paths; splitting would reduce clarity
	// Obtain the original TIFF bytes. tiff.Extract (called during Read) stores
	// the entire TIFF stream in m.rawEXIF; use it when available to avoid a
	// second full-file read.
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = m.rawEXIF
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: tiff seek: %w", err)
		}
		var err error
		originalBytes, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("gometadata: tiff read: %w", err)
		}
	}

	// Encode IPTC and XMP the normal way (nil → pass-through original bytes).
	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	// FormatTIFF is never JPEG, so the XMP wire-frame is never used;
	// encodeXMP returns rawXMP directly for non-JPEG formats.
	rawXMP, err := encodeXMP(m, format.FormatTIFF)
	if err != nil {
		return err
	}

	// If the caller did not modify IPTC or XMP AND did not modify EXIF either,
	// perform a simple pass-through write — no relocation needed.
	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: tiff write passthrough: %w", err)
		}
		return nil
	}

	// Delegate to tiff.InjectWithEXIF which calls relocateTIFFFromParsed with
	// the original bytes as the image-data source and m.EXIF as the IFD model.
	// m.EXIF may be nil (no EXIF modifications); InjectWithEXIF falls back to
	// parsing originalBytes in that case, same behaviour as Inject.
	if err := tiff.InjectWithEXIF(originalBytes, m.EXIF, rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// writeTIFFARW handles metadata injection for Sony ARW files.
//
// ARW is TIFF-based (II\0* little-endian magic) but requires Sony-specific
// handling:
//   - The Sony MakerNote (tag 0x927C) is a plain TIFF IFD with TIFF-absolute
//     offsets, unlike Canon (blob-relative) and Nikon (MakerNote-TIFF-relative).
//     After relocation, all 34 OOL MakerNote entries would have stale offsets,
//     causing the 52 MakerNote tags to be lost.  The ARW write path rebases
//     all MakerNote OOL val_or_off fields by delta = new_blob_abs − old_blob_abs.
//   - The SR2Private (0xC634) entry in IFD0 holds a TIFF-absolute offset (as
//     4 inline bytes) to an SR2 IFD block.  That block (≈37 kB) contains the
//     encrypted SR2SubIFD and an empty IDC_IFD.  It must be copied verbatim,
//     appended to the output, and its internal TIFF-absolute pointers rebased.
//     The 0xC634 inline value in IFD0 is patched post-encode.
//
// The ARW-specific path (tiff.InjectWithEXIFARW) handles both concerns.
// All other aspects (SubIFD relocation, OOL RATIONAL patching, etc.) are
// identical to the standard writeTIFF path.
//
// Validated against real.arw (Sony DSLR-A500, 13 MB): ImageDataHash IN==OUT,
// all metadata including 52 MakerNote tags and SR2Private block preserved.
func writeTIFFARW(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic mirrors writeTIFF; splitting reduces clarity
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = m.rawEXIF
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: arw seek: %w", err)
		}
		var err error
		originalBytes, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("gometadata: arw read: %w", err)
		}
	}

	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	rawXMP, err := encodeXMP(m, format.FormatARW)
	if err != nil {
		return err
	}

	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: arw write passthrough: %w", err)
		}
		return nil
	}

	if err := tiff.InjectWithEXIFARW(originalBytes, m.EXIF, rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// writeTIFFORF handles metadata injection for Olympus ORF files.
//
// ORF is TIFF-based but uses non-standard magic bytes at bytes [2:4]:
//   - IIRO (0x52 0x4F, "RO") — used by Olympus DSLRs (E-series, OM-D line).
//   - IIRS (0x52 0x53, "RS") — used by older Olympus compacts (C-series, SP-series).
//
// orf.Extract patches bytes [2:4] to 0x2A 0x00 when it returns rawEXIF, so
// m.rawEXIF carries patched magic.  writeTIFFORF reads the original magic from r
// and restores it into a working copy of m.rawEXIF before calling InjectWithEXIFORF.
//
// All other aspects (strip/tile relocation, SubIFD relocation, OOL RATIONAL
// patching, etc.) use the standard copy-and-relocate algorithm unchanged,
// because Olympus ORF IFD structure is fully standard TIFF after magic patching.
// Olympus MakerNote uses blob-relative offsets (ExifTool Olympus.pm), so verbatim
// MakerNote copying is safe.
//
// Un-gated in task #104 after real-corpus validation (Olympus E-M10 IIRO,
// Olympus C5050Z IIRS): ImageDataHash IN==OUT, all metadata preserved.
func writeTIFFORF(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic mirrors writeTIFF; splitting reduces clarity
	// Recover the original ORF magic from r (bytes 0-3 of the file).
	// m.rawEXIF carries patched magic (0x2A 0x00 at bytes [2:4]) because
	// orf.Extract patches in-place before returning rawEXIF.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("gometadata: orf seek for magic: %w", err)
	}
	var origMagicBuf [4]byte
	if _, err := io.ReadFull(r, origMagicBuf[:]); err != nil {
		return fmt.Errorf("gometadata: orf read magic: %w", err)
	}

	// Obtain the original ORF bytes for use as the image-data relocation base.
	// Use m.rawEXIF when available (orf.Extract stores the full ORF stream there);
	// fall back to a full read from r.
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = make([]byte, len(m.rawEXIF))
		copy(originalBytes, m.rawEXIF)
		// Restore the real ORF magic (m.rawEXIF has patched bytes [2:4] = 0x2A 0x00).
		if len(originalBytes) >= 4 {
			copy(originalBytes[0:4], origMagicBuf[:])
		}
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: orf seek: %w", err)
		}
		var err error
		originalBytes, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("gometadata: orf read: %w", err)
		}
	}

	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	rawXMP, err := encodeXMP(m, format.FormatORF)
	if err != nil {
		return err
	}

	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: orf write passthrough: %w", err)
		}
		return nil
	}

	if err := tiff.InjectWithEXIFORF(originalBytes, m.EXIF, rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// writeTIFFRW2 handles metadata injection for Panasonic RW2 files.
//
// RW2 is TIFF-based but has two non-standard features:
//
//  1. Non-standard magic "IIU\x00" (0x49 0x49 0x55 0x00) at bytes [0:4].
//     exif.Parse requires 0x002A; the RW2 write path patches bytes [2:4] before
//     parsing and restores the original magic in the output.
//
//  2. 16-byte Panasonic device GUID at bytes [8:24].
//     IFD0 is at offset 24 (not the standard 8). After tiff.InjectWithEXIFRW2
//     (which produces IFD0 at offset 8 via exif.Encode) the GUID is re-inserted
//     at position 8 and all absolute IFD0 OOL pointers are rebased by +16.
//
// rw2.Extract patches bytes [2:4] to 0x2A 0x00 when it returns rawEXIF, so
// writeTIFFRW2 recovers the original magic from r and restores it before calling
// InjectWithEXIFRW2.
//
// Un-gated in task #104 after real-corpus validation (Panasonic DMC-GF1):
// ImageDataHash IN==OUT, JpgFromRaw (0x002E) and raw sensor data preserved.
func writeTIFFRW2(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic mirrors writeTIFF; splitting reduces clarity
	// Recover the original RW2 magic from r.
	// m.rawEXIF carries patched magic (0x2A 0x00 at bytes [2:4]).
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("gometadata: rw2 seek for magic: %w", err)
	}
	var origMagicBuf [4]byte
	if _, err := io.ReadFull(r, origMagicBuf[:]); err != nil {
		return fmt.Errorf("gometadata: rw2 read magic: %w", err)
	}

	// Obtain the original RW2 bytes for use as the image-data relocation base.
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = make([]byte, len(m.rawEXIF))
		copy(originalBytes, m.rawEXIF)
		// Restore the real RW2 magic (m.rawEXIF has patched bytes [2:4] = 0x2A 0x00).
		if len(originalBytes) >= 4 {
			copy(originalBytes[0:4], origMagicBuf[:])
		}
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: rw2 seek: %w", err)
		}
		var err error
		originalBytes, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("gometadata: rw2 read: %w", err)
		}
	}

	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	rawXMP, err := encodeXMP(m, format.FormatRW2)
	if err != nil {
		return err
	}

	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: rw2 write passthrough: %w", err)
		}
		return nil
	}

	if err := tiff.InjectWithEXIFRW2(originalBytes, m.EXIF, rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// encodeMetadata serialises each modified metadata segment. If a segment was
// not modified (m.EXIF/IPTC/XMP is nil) the original raw bytes are passed
// through unchanged. Returns the first encoding error encountered.
//
// fmtID is the detected destination container format. It is forwarded to
// encodeXMP so that the JPEG extended-XMP wire-frame is never handed to a
// non-JPEG injector.
func encodeMetadata(m *Metadata, fmtID format.FormatID) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if rawEXIF, err = encodeEXIF(m); err != nil {
		return nil, nil, nil, err
	}
	if rawIPTC, err = encodeIPTC(m); err != nil {
		return nil, nil, nil, err
	}
	if rawXMP, err = encodeXMP(m, fmtID); err != nil {
		return nil, nil, nil, err
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// encodeEXIF returns the EXIF bytes to write: freshly encoded when m.EXIF is
// non-nil, or the original raw bytes when the caller did not modify EXIF.
func encodeEXIF(m *Metadata) ([]byte, error) {
	if m.EXIF != nil {
		raw, err := exif.Encode(m.EXIF)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode EXIF: %w", err)
		}
		return raw, nil
	}
	return m.rawEXIF, nil
}

// encodeIPTC returns the IPTC bytes to write: freshly encoded when m.IPTC is
// non-nil, or the original raw bytes when the caller did not modify IPTC.
func encodeIPTC(m *Metadata) ([]byte, error) {
	if m.IPTC != nil {
		raw, err := iptc.Encode(m.IPTC)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode IPTC: %w", err)
		}
		return raw, nil
	}
	return m.rawIPTC, nil
}

// encodeXMP returns the XMP bytes to write: freshly encoded when m.XMP is
// non-nil, the wire-frame encoding when the image carried extended XMP and the
// XMP was not modified (guarantees byte-stable round-trips for JPEG), or the
// original raw bytes otherwise.
//
// fmtID is the destination container format. The wire-frame encoding is only
// valid for JPEG: jpeg.Inject is the only injector that knows how to decode it
// (via writeXMPSegments → decodeXMPWire). For every other container format the
// wire-frame is bypassed and rawXMP (the reassembled, user-visible packet) is
// returned instead, preventing a corrupt XMP blob from being written to
// PNG/WebP/HEIF/AVIF and other non-JPEG containers.
//
// See task #70: Extended-XMP wire-frame leaks to PNG/WebP/HEIF inject.
func encodeXMP(m *Metadata, fmtID format.FormatID) ([]byte, error) {
	if m.XMP != nil {
		raw, err := xmppkg.Encode(m.XMP)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode XMP: %w", err)
		}
		return raw, nil
	}
	// The wire-frame encoding (rawXMPWire) carries the original JPEG main APP1
	// content and the assembled extended XMP payload packed together. Only
	// jpeg.Inject can decode it — passing it to any other injector would write
	// the raw wire-frame bytes verbatim as the XMP packet, producing a corrupt
	// non-XMP blob in the destination container.
	//
	// Return rawXMPWire only when the destination format is JPEG; fall back to
	// rawXMP (the fully reassembled, user-visible packet) for all other formats.
	if m.rawXMPWire != nil && fmtID == format.FormatJPEG {
		return m.rawXMPWire, nil
	}
	return m.rawXMP, nil
}

// injectByFormat dispatches to the correct container handler for segment injection.
// preserveUnknownSegments is forwarded to the format-specific injector: JPEG
// honours it by dropping unknown APPn segments when false; PNG, WebP, HEIF/AVIF,
// and CR3 return ErrPreserveUnknownSegmentsNotSupported when false. TIFF-based
// formats are dispatched by their dedicated write functions in Write() before
// this function is ever reached; their entries in the injectors map are not
// used by the top-level write path.
func injectByFormat(r io.ReadSeeker, w io.Writer, fmtID format.FormatID, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	fn, ok := injectors[fmtID]
	if !ok {
		return &UnsupportedFormatError{}
	}
	return wrapInject(fn(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments))
}

// wrapInject wraps errors from format-specific Inject calls with the library prefix.
func wrapInject(err error) error {
	if err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// writeTIFFNEF handles metadata injection for Nikon NEF files.
//
// NEF is TIFF-based (MM\0* big-endian magic) but requires Nikon-specific
// handling:
//   - The Nikon Type-3 MakerNote (tag 0x927C) declares a 9705-byte blob but its
//     internal TIFF references PreviewIFD and NikonScanIFD that live beyond the
//     declared extent.  The MakerNote blob must be extended before encoding so
//     those structures are preserved in the output.
//   - PreviewIFD (MakerNote tag 0x0011) references a preview JPEG at a
//     MakerNote-TIFF-relative offset.  That image block must be enumerated and
//     relocated, and the offset patched in the MakerNote after encoding.
//
// The NEF-specific path (tiff.InjectWithEXIFNEF) handles both concerns.
// All other aspects (SubIFD relocation, OOL RATIONAL patching, etc.) are
// identical to the standard writeTIFF path.
//
// Validated against real.nef (Nikon D70): ImageDataHash IN==OUT, all metadata
// including PreviewIFD and NikonScanIFD preserved, file size unchanged.
func writeTIFFNEF(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic mirrors writeTIFF; splitting reduces clarity
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = m.rawEXIF
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: nef seek: %w", err)
		}
		var err error
		originalBytes, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("gometadata: nef read: %w", err)
		}
	}

	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	rawXMP, err := encodeXMP(m, format.FormatNEF)
	if err != nil {
		return err
	}

	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: nef write passthrough: %w", err)
		}
		return nil
	}

	if err := tiff.InjectWithEXIFNEF(originalBytes, m.EXIF, rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}
