package tiff

// relocate_orf.go — Olympus ORF-specific copy-and-relocate entry point (task #104).
//
// Problem: Olympus ORF files use non-standard TIFF magic bytes at positions [2:4]:
//   - IIRO (bytes 0-3: 0x49 0x49 0x52 0x4F) — used by Olympus DSLRs (E-series, OM-D)
//   - IIRS (bytes 0-3: 0x49 0x49 0x52 0x53) — used by older Olympus compacts
//     (C5050Z, C8080, SP350, SP500UZ)
//
// exif.Parse rejects both because it requires magic == 0x002A (TIFF 6.0 §2).
// The IFD0 offset and all internal structure are otherwise standard TIFF-compatible:
// IFD0 is at the standard offset stored in bytes [4:8].
//
// Fix (task #104):
//
// InjectWithEXIFORF patches bytes [2:4] to 0x2A 0x00 in a working copy of the
// original bytes, delegates to the standard relocateTIFFFromParsed algorithm,
// and restores the caller-supplied ORF magic in the output bytes [0:4].
//
// The caller (writeTIFFORF in write.go) is responsible for:
//   - Providing originalBytes that are either raw ORF bytes (bytes 2-3 = 'R' + variant)
//     or rawEXIF bytes already patched by orf.Extract (bytes 2-3 = 0x2A 0x00).
//   - Providing originalMagic — the 4 original ORF bytes (IIRO or IIRS) — so that
//     the write path can restore the correct variant regardless of whether rawEXIF
//     is used as the relocation base.
//
// Spec references:
//   - TIFF 6.0 §2: TIFF header layout.
//   - ExifTool Olympus.pm: ORF magic bytes and structure.
//   - task #104: ORF write un-gating.

import (
	"errors"
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Sentinel errors for the ORF-specific relocation subsystem.
var (
	// ErrORFInvalidMagic is returned when the base bytes do not carry a
	// recognised Olympus ORF magic at bytes [0:4].
	ErrORFInvalidMagic = errors.New("tiff: ORF invalid magic bytes (expected IIRO or IIRS)")

	// ErrORFOutputTooShort is returned when the assembled ORF output is shorter
	// than the minimum 4 bytes needed to restore the ORF magic.
	ErrORFOutputTooShort = errors.New("tiff: ORF output too short to restore magic bytes")
)

// isORFMagic reports whether the 4-byte slice b carries a valid Olympus ORF magic.
// Accepts both IIRO (0x49 0x49 0x52 0x4F) and IIRS (0x49 0x49 0x52 0x53).
//
// ExifTool Olympus.pm: ORFMagic is "IIRO" or "IIRS".
func isORFMagic(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// Bytes 0-1 are always "II" (little-endian byte-order marker).
	// Byte 2 is always 'R' (0x52).
	// Byte 3 is 'O' (0x4F) for IIRO or 'S' (0x53) for IIRS.
	return b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x52 &&
		(b[3] == 0x4F || b[3] == 0x53)
}

// relocateTIFFAsORF is the internal ORF relocator entry point used by the tiff.go
// wrapper InjectWithEXIFORF.
//
// originalBytes must carry a valid ORF magic at bytes [0:4] (isORFMagic must
// return true). The caller (writeTIFFORF in write.go) is responsible for
// restoring the real ORF magic into the bytes before calling this function,
// since orf.Extract patches bytes [2:4] to 0x2A 0x00 when it returns rawEXIF.
//
// This separation (internal relocator + thin tiff.go wrapper) matches the
// InjectWithEXIFNEF / InjectWithEXIFARW pattern.
func relocateTIFFAsORF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	// Make a working copy so we can patch bytes [2:4] in-place without
	// mutating the caller's buffer (rawEXIF may be shared).
	workBytes := make([]byte, len(originalBytes))
	copy(workBytes, originalBytes)
	return relocateTIFFFromParsedORF(workBytes, modifiedEXIF, rawIPTC, rawXMP)
}

// relocateTIFFFromParsedORF patches the ORF magic, runs the standard TIFF
// copy-and-relocate algorithm, and restores the original ORF magic in the output.
//
// base must carry a valid ORF magic at bytes [0:4] (isORFMagic must return true).
// base is mutated in-place (bytes [2:4] are patched); callers must pass a
// writable copy — InjectWithEXIFORF always does this.
func relocateTIFFFromParsedORF(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	if !isORFMagic(base) {
		return nil, fmt.Errorf("orf: %w", ErrORFInvalidMagic)
	}

	// Save the original 4-byte ORF magic for restoration in the output.
	var origMagic [4]byte
	copy(origMagic[:], base[0:4])

	// Patch bytes [2:4] to standard TIFF LE magic (0x2A 0x00).
	//
	// TIFF 6.0 §2: magic must be 0x002A for classic TIFF. exif.Parse requires
	// this. The IFD0 offset at bytes [4:8] is already a valid standard TIFF offset.
	base[2] = 0x2A
	base[3] = 0x00

	// Parse the magic-patched base when no pre-parsed struct is provided.
	if e == nil {
		var parseErr error
		e, parseErr = exif.Parse(base)
		if parseErr != nil {
			return nil, fmt.Errorf("orf: parse for relocation: %w", parseErr)
		}
	}

	// Delegate to the standard TIFF copy-and-relocate algorithm.
	//
	// Olympus ORF IFD structure is fully standard TIFF after magic patching:
	//   - IFD0 at bytes [4:8] offset (standard).
	//   - Strip data via StripOffsets/StripByteCounts (0x0111/0x0117).
	//   - Multi-strip arrays in older ORF (e.g. C5050Z has 122 strips).
	//   - No extra header bytes (unlike Panasonic RW2 which has a 16-byte GUID).
	//   - Olympus MakerNote uses blob-relative offsets per ExifTool Olympus.pm,
	//     so verbatim MakerNote copying (the standard relocator's policy) is safe.
	//
	// ExifTool Olympus.pm: ORF MakerNote is self-contained (blob-relative offsets).
	finalTIFF, err := relocateTIFFFromParsed(base, e, rawIPTC, rawXMP)
	if err != nil {
		return nil, fmt.Errorf("orf: relocate: %w", err)
	}

	// Restore the original ORF magic in the output bytes [0:4].
	//
	// exif.Encode produces a standard TIFF header: "II" + 0x2A 0x00 + IFD0_off.
	// Replace bytes [0:4] with the saved ORF magic (IIRO or IIRS) so the output
	// is recognised as a valid Olympus ORF by all ORF-aware tools and cameras.
	if len(finalTIFF) < 4 {
		return nil, fmt.Errorf("orf: %w (%d bytes)", ErrORFOutputTooShort, len(finalTIFF))
	}
	copy(finalTIFF[0:4], origMagic[:])

	return finalTIFF, nil
}
