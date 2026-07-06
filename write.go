package gometadata

import (
	"bytes"
	"encoding/binary"
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
//
// Trust boundary (#259): Write streams directly to w as it encodes; it does
// not buffer the complete result before writing. If an error occurs after
// some bytes have already been written, w may already contain partial or
// inconsistent output. Callers that need all-or-nothing semantics — the
// destination is either the complete, correct result or is left untouched —
// must use WriteFile, which stages output in a temporary file and only
// replaces the target via an atomic rename after a complete, synced write
// succeeds.
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

	// Guard: reject cross-format writes when the write target is a TIFF-based
	// container and m was sourced from a different format.
	//
	// #108: the TIFF-family write paths (writeTIFF, writeTIFFNEF, writeTIFFARW,
	// writeTIFFCR2, writeTIFFORF, writeTIFFRW2) use m.rawEXIF as the relocation
	// base — the source of all image-data blocks. When m was read from a JPEG,
	// m.rawEXIF holds the JPEG EXIF blob (typically ~KBs); using it as the base
	// for a TIFF write silently discards the TIFF's image data and produces a
	// corrupt output indistinguishable from a valid write (err==nil).
	//
	// Non-TIFF targets (JPEG, PNG, WebP, HEIF/AVIF, CR3) are safe: their inject
	// paths re-encode metadata from scratch via encodeMetadata and never use
	// m.rawEXIF as a binary relocation base. Cross-format transcoding from (e.g.)
	// a JPEG source to PNG/WebP is a legitimate use-case and is preserved.
	//
	// FormatUnknown is excluded: m may have been constructed via NewMetadata or
	// assembled without reading an existing file.
	//
	// Note: this guard fires before any I/O on w is attempted, so no partial
	// output is ever written to the caller's io.Writer.
	if isTIFFBasedFormat(fmtID) {
		if mFmt := format.FormatID(m.format); mFmt != format.FormatUnknown && mFmt != fmtID {
			return fmt.Errorf("gometadata: cannot write %s metadata into a %s container: %w",
				mFmt, fmtID, ErrFormatMismatch)
		}
	}

	// All TIFF-based containers use dedicated write paths that keep the ORIGINAL
	// TIFF bytes as the image-data relocation base (epic #33 Option A).  Passing
	// an exif.Encode-produced skeleton as the relocation base causes
	// ErrBlockOutOfBounds because the skeleton carries no image blocks (task #97).
	//
	// FormatTIFF, FormatDNG: standard copy-and-relocate (writeTIFF).
	// FormatCR2: same relocation base, but bytes 8–11 (Canon "CR\02\00" marker)
	//   must be restored after encoding — InjectWithEXIFCR2 handles this.
	// FormatNEF: Nikon MakerNote blob extension + PreviewIFD relocation (writeTIFFNEF, task #102).
	// FormatARW: Sony MakerNote TIFF-absolute rebase + SR2Private block (writeTIFFARW, task #103).
	// FormatORF: non-standard IIRO/IIRS magic patch-and-restore (writeTIFFORF, task #104).
	// FormatRW2: Panasonic GUID insertion + IFD0 offset rebasing (writeTIFFRW2, task #104).
	if fmtID == format.FormatTIFF || fmtID == format.FormatDNG {
		return writeTIFF(r, w, m)
	}
	if fmtID == format.FormatCR2 {
		return writeTIFFCR2(r, w, m)
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
// The temporary file is created in the same directory as the real target so
// that the final os.Rename is always an intra-filesystem operation. This
// prevents EXDEV errors that occur when the system's default temp directory
// ($TMPDIR) lives on a different filesystem (e.g., in containers or NAS
// mounts).
//
// Symlink handling (#125): when path is a symbolic link, WriteFile resolves
// the real file path with filepath.EvalSymlinks before creating the temp file
// and performing the rename. This ensures the rename replaces the real file
// rather than replacing the symlink itself with a regular file. After a
// successful WriteFile on a symlink, the symlink still points at the same
// real path and that real file contains the updated metadata.
//
// Trust boundary (#259): symlink resolution above is intentional and is not
// a vulnerability to be fixed — it is the correct behavior for the common
// case of rewriting metadata on a file reached through a symlink. It does
// mean that WriteFile will follow a symlink to wherever it points, including
// outside any directory tree the caller may have intended to restrict
// writes to. WriteFile performs no path-safety validation of its own:
// callers that pass a path derived from an untrusted source (user input, a
// web request parameter, an archive entry name, etc.) must validate or
// reject symlinks and other unsafe path constructions themselves before
// calling WriteFile, exactly as they would before any other path-based file
// operation such as os.OpenFile.
//
// Durability (#124): WriteFile calls Sync on the temp file after all data has
// been written and before Close/Rename. If Sync fails the function aborts,
// removes the temp file, and returns the error so the original file is left
// intact. After a successful Rename, the parent directory is fsynced
// (best-effort, errors silently ignored) to commit the directory entry to
// durable storage.
//
// Ownership preservation (#125): on Unix, WriteFile attempts to chown the
// temp file to the uid/gid of the original file before the rename. This is
// best-effort: EPERM and unsupported-filesystem errors are silently ignored.
//
// Privilege-bit masking (#259): the replacement file's ordinary permission
// bits (owner/group/other read-write-execute) are preserved, but setuid,
// setgid, and the sticky bit are always cleared on the replacement, even
// when the original file carried them. A metadata rewrite must never
// (re)create a privilege-escalation surface.
func WriteFile(path string, m *Metadata, opts ...WriteOption) error { //nolint:cyclop,gocyclo // linear sequence of OS calls with early-exit error handling; splitting would reduce clarity
	// Resolve symlinks so that the rename target is the real file, not the
	// symlink itself. filepath.EvalSymlinks returns the original path unchanged
	// when path is not a symlink, so the non-symlink case is handled identically.
	// Audit finding #125: WriteFile must not replace a symlink with a regular file.
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		// EvalSymlinks fails for dangling symlinks or missing paths; fall through
		// to os.Open which will surface the same error with the correct context.
		realPath = path
	}

	f, err := os.Open(realPath)
	if err != nil {
		return fmt.Errorf("gometadata: open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("gometadata: stat file: %w", err)
	}

	// Place the temp file in the same directory as the real target so the
	// eventual rename is guaranteed to be atomic (same filesystem, no EXDEV).
	dir := filepath.Dir(realPath)
	tmp, err := os.CreateTemp(dir, "gometadata-*")
	if err != nil {
		return fmt.Errorf("gometadata: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// renamed tracks whether os.Rename has successfully moved tmpName to
	// realPath. The deferred cleanup removes the temp file only when it is
	// still on disk (i.e., rename has not yet occurred or has failed).
	// os.Remove on a non-existent path returns an error that we silently
	// ignore, making this safe to call unconditionally.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	// Preserve original file permissions before writing any data, but mask off
	// setuid/setgid/sticky (#259): (*os.File).Chmod maps os.ModeSetuid,
	// os.ModeSetgid, and os.ModeSticky straight through to S_ISUID/S_ISGID/
	// S_ISVTX, so a source file that happened to carry any of these bits would
	// otherwise cause the re-encoded replacement to carry them too. This is
	// CWE-732-adjacent hardening: a metadata rewrite must never (re)create a
	// privilege-escalation surface, even when it is preservation-only and no
	// worse than the bit already present on the original file.
	safeMode := fi.Mode() &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err := tmp.Chmod(safeMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gometadata: chmod temp file: %w", err)
	}

	// Best-effort ownership preservation on Unix (#125): transfer uid/gid from
	// the original file to the temp file before the rename. EPERM and
	// unsupported-filesystem errors are silently ignored by chownFile.
	chownFile(tmp, f)

	if err := Write(f, tmp, m, opts...); err != nil {
		_ = tmp.Close()
		return err
	}

	// Flush the temp file to durable storage before rename (#124).
	// If Sync fails we abort WITHOUT renaming so the original file is left
	// intact. The deferred Remove will clean up the temp file.
	// Audit finding #124: a crash between write and sync can leave a truncated
	// temp file; syncing before rename guarantees the replacement is complete.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gometadata: sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gometadata: close temp file: %w", err)
	}

	if err := os.Rename(tmpName, realPath); err != nil {
		return fmt.Errorf("gometadata: rename temp file: %w", err)
	}
	renamed = true

	// Best-effort directory fsync: flush the directory entry created by Rename
	// to durable storage so the renamed file is visible after a crash (#124).
	// Errors are silently ignored: some filesystems (tmpfs, FAT32) and some
	// configurations do not support directory fsync.
	fsyncDir(dir)

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
// isBigTIFFSource reports whether the raw EXIF bytes or the parsed EXIF struct
// indicate a BigTIFF source (magic 0x002B at bytes [2:4] in the appropriate
// byte order). It is used as a fast-fail guard at the top of every TIFF write
// path so that BigTIFF sources return ErrWriteNotSupported before any I/O.
//
// The check is in two layers:
//  1. m.EXIF.BigTIFF — set by exif.Parse for BigTIFF sources; authoritative
//     when a parsed EXIF is present.
//  2. raw bytes — used as a fallback when m.EXIF is nil (pure IPTC/XMP change
//     with no EXIF edits); checks bytes [0:4] for the BigTIFF magic 0x002B.
//
// BigTIFF spec §2 (Aware Systems / libtiff); audit finding #107.
func isBigTIFFSource(raw []byte, e *exif.EXIF) bool {
	if e != nil && e.BigTIFF {
		return true
	}
	// raw bytes: validate minimum length and determine byte order before reading magic.
	if len(raw) < 4 {
		return false
	}
	// TIFF 6.0 §2: byte order marker "II" (LE) or "MM" (BE) at bytes [0:2].
	// BigTIFF spec §2: magic 0x002B at bytes [2:4] (same position as classic TIFF).
	switch {
	case raw[0] == 'I' && raw[1] == 'I':
		return binary.LittleEndian.Uint16(raw[2:]) == 0x002B
	case raw[0] == 'M' && raw[1] == 'M':
		return binary.BigEndian.Uint16(raw[2:]) == 0x002B
	}
	return false
}

// readAllCapped reads all of r, capping the total to maxFileSize+1 bytes via
// io.LimitReader so that an oversized or infinite streaming reader cannot
// trigger unbounded heap allocation.
//
// Security audit FIX 3 (CWE-770/400): every writeTIFF* entry point falls back
// to a bare io.ReadAll(r) when m.rawEXIF is nil (the caller constructed
// *Metadata via NewMetadata rather than Read). #140 already capped every
// io.ReadAll in the format/* packages (format/tiff, format/heif, ...) with
// this identical pattern; these six root-package call sites were missed.
// tag identifies the calling write path (e.g. "tiff", "cr2") for the wrapped
// error message.
func readAllCapped(r io.Reader, tag string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("gometadata: %s read: %w", tag, err)
	}
	if int64(len(data)) > maxFileSize {
		return nil, fmt.Errorf("gometadata: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	return data, nil
}

func writeTIFF(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // conditional logic for originalBytes source + nil guards + three encode paths; splitting would reduce clarity
	// BigTIFF guard: reject BigTIFF sources before any I/O.
	// exif.Encode would return ErrBigTIFFEncodeNotSupported deep inside the
	// relocator, after partial work. Surface it here as ErrWriteNotSupported
	// so the caller sees a clear, actionable error and zero bytes are written.
	// BigTIFF spec §2; audit finding #107.
	if isBigTIFFSource(m.rawEXIF, m.EXIF) {
		return fmt.Errorf("gometadata: writing metadata into a BigTIFF container is not yet supported (BigTIFF write requires a native 64-bit encoder): %w", ErrWriteNotSupported)
	}

	// Obtain the original TIFF bytes. tiff.Extract (called during Read) stores
	// the entire TIFF stream in m.rawEXIF; use it when available to avoid a
	// second full-file read.
	//
	// #139: use a defensive copy of m.rawEXIF so that caller mutations of the
	// slice returned by RawEXIF() do not affect the relocation base. This is
	// consistent with writeTIFFORF/writeTIFFRW2 which already copy m.rawEXIF.
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = bytes.Clone(m.rawEXIF)
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: tiff seek: %w", err)
		}
		var err error
		originalBytes, err = readAllCapped(r, "tiff")
		if err != nil {
			return err
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
	//
	// #109: pass a deep clone of m.EXIF so that relocateTIFFFromParsed's
	// structural mutations (ThumbnailData clear, Entries slice rewrite for
	// strip/tile placeholders, IPTC/XMP upsert) do not permanently alter the
	// caller's *Metadata.  A second Write call on the same *Metadata would
	// otherwise see a corrupted IFD0 (missing strip offsets, cleared thumbnail,
	// altered entry count) and produce wrong output or an ErrBlockOutOfBounds.
	if err := tiff.InjectWithEXIF(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// writeTIFFCR2 handles metadata injection for Canon CR2 files.
//
// CR2 uses standard TIFF LE magic (II*\0) and the standard copy-and-relocate
// path, but bytes 8–11 of the TIFF header carry the proprietary Canon CR2 marker:
//   - bytes 8–9:   0x43 0x52 ('C','R') — Canon CR2 spec §3.1
//   - bytes 10–11: 0x02 0x00           — CR2 version 2.0
//
// exif.Encode rebuilds the TIFF with IFD0 at offset 8, overwriting those bytes.
// tiff.InjectWithEXIFCR2 restores them from the original file after relocation.
//
// containers.md §8(e): "CR2: preserve CR 02 00 at offset 8."
// Validated against real Canon EOS 350D/70D/7D corpus files.
func writeTIFFCR2(r io.ReadSeeker, w io.Writer, m *Metadata) error { //nolint:cyclop,gocyclo // mirrors writeTIFF; CR2-specific marker-restore call; splitting reduces clarity
	// #139: defensive copy of m.rawEXIF (consistent with writeTIFF, writeTIFFORF, writeTIFFRW2).
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = bytes.Clone(m.rawEXIF)
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: cr2 seek: %w", err)
		}
		var err error
		originalBytes, err = readAllCapped(r, "cr2")
		if err != nil {
			return err
		}
	}

	rawIPTC, err := encodeIPTC(m)
	if err != nil {
		return err
	}
	rawXMP, err := encodeXMP(m, format.FormatCR2)
	if err != nil {
		return err
	}

	if rawIPTC == nil && rawXMP == nil && m.EXIF == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("gometadata: cr2 write passthrough: %w", err)
		}
		return nil
	}

	// #109: pass a deep clone of m.EXIF (see writeTIFF for rationale).
	if err := tiff.InjectWithEXIFCR2(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
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
	// #139: defensive copy of m.rawEXIF (consistent with writeTIFF, writeTIFFORF, writeTIFFRW2).
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = bytes.Clone(m.rawEXIF)
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: arw seek: %w", err)
		}
		var err error
		originalBytes, err = readAllCapped(r, "arw")
		if err != nil {
			return err
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

	// #109: pass a deep clone of m.EXIF (see writeTIFF for rationale).
	if err := tiff.InjectWithEXIFARW(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
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
// #117 fix: orf.Extract now returns rawEXIF with the ORIGINAL magic preserved.
// writeTIFFORF reads the original magic from r (which is authoritative) and
// stores it into the working copy of m.rawEXIF so InjectWithEXIFORF can patch
// to standard TIFF magic for the relocation pass and restore the ORF magic after.
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
		originalBytes, err = readAllCapped(r, "orf")
		if err != nil {
			return err
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

	// Security audit FIX 2: pass a clone, not m.EXIF directly.
	// relocateTIFFFromParsedORF permanently mutates the *exif.EXIF it is given
	// (clears ThumbnailData, rewrites StripOffsets/StripByteCounts/MakerNote
	// pointer entries with relocated offsets). Every sibling path already
	// clones (writeTIFF, writeTIFFCR2, writeTIFFARW, writeTIFFNEF — the #109
	// fix); ORF was added later in #104 and missed the clone, so a second
	// Write() call on the same *Metadata reused the already-mutated EXIF and
	// silently corrupted the output image data.
	if err := tiff.InjectWithEXIFORF(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
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
// #117 fix: rw2.Extract now returns rawEXIF with the ORIGINAL magic preserved.
// writeTIFFRW2 reads the original magic from r (which is authoritative) and
// stores it into the working copy of m.rawEXIF so InjectWithEXIFRW2 can patch
// to standard TIFF magic for the relocation pass and restore the RW2 magic after.
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
		originalBytes, err = readAllCapped(r, "rw2")
		if err != nil {
			return err
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

	// Security audit FIX 2: pass a clone, not m.EXIF directly. See the
	// identical comment in writeTIFFORF above for the full rationale
	// (relocateTIFFFromParsedRW2 permanently mutates its *exif.EXIF argument).
	if err := tiff.InjectWithEXIFRW2(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
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
//
// #190 fix: a zero-length result from iptc.Encode (which occurs when m.IPTC is
// a non-nil but structurally empty *iptc.IPTC with no datasets) is normalised to
// nil. This prevents downstream upsert logic from writing a zero-length 0x83BB
// tag into the TIFF IFD when the caller merely set m.IPTC = new(iptc.IPTC)
// without adding any datasets. Nil signals "no IPTC to write"; a non-nil empty
// slice is an encoding artifact that must not be stored as a tag value.
func encodeIPTC(m *Metadata) ([]byte, error) {
	if m.IPTC != nil {
		raw, err := iptc.Encode(m.IPTC)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode IPTC: %w", err)
		}
		if len(raw) == 0 {
			return nil, nil // normalise empty encoding to nil (no IPTC to write)
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

// isTIFFBasedFormat reports whether fmtID is a TIFF-based container format.
//
// TIFF-based formats route through writeTIFF / writeTIFFNEF / writeTIFFARW /
// writeTIFFCR2 / writeTIFFORF / writeTIFFRW2, all of which use m.rawEXIF as
// the relocation base (the byte source for image-data blocks).  Non-TIFF
// formats use encodeMetadata + injectByFormat, which re-encode metadata from
// scratch and never touch m.rawEXIF.
func isTIFFBasedFormat(f format.FormatID) bool {
	switch f { //nolint:exhaustive // deliberate membership test; all other FormatIDs are non-TIFF
	case format.FormatTIFF, format.FormatDNG, format.FormatCR2,
		format.FormatNEF, format.FormatARW, format.FormatORF, format.FormatRW2:
		return true
	}
	return false
}

// cloneEXIF returns a deep clone of e that is safe to pass to relocateTIFFFromParsed
// (and its NEF/ARW/CR2 variants) without mutating the caller's *Metadata.
//
// #109: relocateTIFFFromParsed permanently mutates the *exif.EXIF it receives:
//   - e.IFD0.ThumbnailData is set to nil (step 2.5).
//   - e.IFD0.Entries (and entries in other IFDs) are modified by
//     removeImageOffsetEntries (in-place slice trimming) and
//     insertPlaceholders/upsertIFDEntryWithCount (in-place append).
//   - upsertIFD0Entry appends IPTC/XMP entries to e.IFD0.Entries.
//
// A second Write call on the same *Metadata after an un-cloned first Write
// would see a corrupted IFD0 (strip offsets removed, ThumbnailData nil, IPTC
// entry duplicated) and fail with ErrBlockOutOfBounds or produce wrong output.
//
// Clone strategy:
//   - Each IFD's Entries slice is copied into a new slice so that appends and
//     removals performed by the relocator do not affect the original.
//   - IFDEntry.Value byte slices are NOT copied: the relocator never mutates
//     value bytes in-place; it only replaces whole Entries elements.
//   - ThumbnailData is copied (bytes.Clone) so the nil assignment in step 2.5
//     does not clear the original IFD's cached thumbnail.
//   - The IFD chain (IFD.Next) is cloned recursively because removeImageOffsetEntries
//     can reach any IFD in the chain via enumerateImageBlocks.
//   - EXIF sub-IFDs (ExifIFD, GPSIFD, InteropIFD, MakerNoteIFD) are cloned
//     because upsertIFDEntryWithCount is called on IFDs owned by mainBlocks,
//     which may include the EXIF sub-IFD chain.
//   - MakerNote bytes are NOT copied: verbatim-copy semantics are unchanged
//     (the blob is never mutated, only its IFD-offset entry is rewritten).
//
// nil input → nil output (safe for callers that pass m.EXIF when m.EXIF is nil).
func cloneEXIF(e *exif.EXIF) *exif.EXIF {
	if e == nil {
		return nil
	}
	out := &exif.EXIF{
		ByteOrder:         e.ByteOrder,
		IFD0:              cloneIFD(e.IFD0),
		ExifIFD:           cloneIFD(e.ExifIFD),
		GPSIFD:            cloneIFD(e.GPSIFD),
		InteropIFD:        cloneIFD(e.InteropIFD),
		MakerNote:         e.MakerNote, // verbatim share — never mutated in-place
		MakerNoteIFD:      cloneIFD(e.MakerNoteIFD),
		MakerNoteOffset:   e.MakerNoteOffset,
		MakerNoteOffset64: e.MakerNoteOffset64,
		// BigTIFF provenance must be preserved so the Encode guard fires even
		// when the clone (not the original) is passed to relocateTIFFFromParsed.
		// BigTIFF spec §2; audit finding #107.
		BigTIFF: e.BigTIFF,
	}
	return out
}

// cloneIFD returns a shallow-entry-slice clone of ifd: a new *IFD value whose
// Entries slice is an independent copy (so appends/removes don't affect src),
// and whose ThumbnailData is a defensive copy (so the nil-clear in step 2.5 of
// relocateTIFFFromParsed does not clear the original's cached thumbnail bytes).
//
// The Next chain is cloned recursively. IFDEntry.Value byte slices are shared
// (not copied) because the relocator never mutates value bytes in-place.
func cloneIFD(ifd *exif.IFD) *exif.IFD {
	if ifd == nil {
		return nil
	}
	entries := make([]exif.IFDEntry, len(ifd.Entries))
	copy(entries, ifd.Entries)
	return &exif.IFD{
		Entries:       entries,
		Next:          cloneIFD(ifd.Next),
		ThumbnailData: bytes.Clone(ifd.ThumbnailData),
	}
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
	// #139: defensive copy of m.rawEXIF (consistent with writeTIFF, writeTIFFORF, writeTIFFRW2).
	var originalBytes []byte
	if m.rawEXIF != nil {
		originalBytes = bytes.Clone(m.rawEXIF)
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("gometadata: nef seek: %w", err)
		}
		var err error
		originalBytes, err = readAllCapped(r, "nef")
		if err != nil {
			return err
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

	// #109: pass a deep clone of m.EXIF (see writeTIFF for rationale).
	if err := tiff.InjectWithEXIFNEF(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w); err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}
