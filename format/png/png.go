// Package png implements extraction and injection of EXIF and XMP metadata
// within PNG files.
//
// PNG structure: 8-byte signature followed by chunks, each with:
// 4-byte length + 4-byte type + <length> bytes data + 4-byte CRC.
//
// Relevant chunks:
//   - eXIf (PNG Extension, registered 2017): raw EXIF payload.
//   - iTXt with keyword "XML:com.adobe.xmp": XMP packet (RFC 2083 §12.13).
package png

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sync"
)

// pngSig is the 8-byte PNG file signature (PNG §5.2).
var pngSig = [8]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} //nolint:gochecknoglobals // package-level constant bytes

// xmpKeyword is the iTXt keyword used by Adobe XMP (XMP Part 3 §1.1.4).
const xmpKeyword = "XML:com.adobe.xmp"

// zlibPool stores reusable io.ReadCloser values (zlib.NewReader return type).
// Reusing them via zlib.Resetter avoids the ~32 KB internal decompression-state
// allocation on every call to zlibDecompress.
var zlibPool sync.Pool //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// zlibDecompress decompresses a zlib-deflated payload. It gets a reader from
// zlibPool (or allocates one) and returns it to the pool when done without
// closing it, so the next caller can Reset it instead of allocating again.
func zlibDecompress(data []byte) ([]byte, error) {
	r := bytes.NewReader(data)
	var zr io.ReadCloser
	if v := zlibPool.Get(); v != nil {
		zr = v.(io.ReadCloser)                                   //nolint:forcetypeassert // zlibPool.New always stores io.ReadCloser; pool invariant
		if err := zr.(zlib.Resetter).Reset(r, nil); err != nil { //nolint:forcetypeassert // zlib.NewReader always implements zlib.Resetter; Go stdlib guarantee
			return nil, fmt.Errorf("png: zlib reset: %w", err)
		}
	} else {
		var err error
		zr, err = zlib.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("png: zlib open: %w", err)
		}
	}
	// Return to pool without closing so it can be Reset on next use.
	defer zlibPool.Put(zr)
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("png: zlib decompress: %w", err)
	}
	return data, nil
}

// processExtractChunk dispatches a single PNG chunk during Extract, updating
// rawEXIF and rawXMP as appropriate. It returns (rawEXIF, rawXMP, done, err)
// where done is true when IEND signals the end of the chunk stream.
func processExtractChunk(chunkType string, data, rawEXIF, rawXMP []byte) ([]byte, []byte, bool, error) {
	switch chunkType {
	case "eXIf":
		return data, rawXMP, false, nil
	case "iTXt", "tEXt", "zTXt":
		xmp, err := handleXMPChunk(chunkType, data, rawXMP)
		if err != nil {
			return rawEXIF, rawXMP, false, err
		}
		return rawEXIF, xmp, false, nil
	case "IEND":
		return rawEXIF, rawXMP, true, nil
	}
	return rawEXIF, rawXMP, false, nil
}

// Extract reads the PNG chunk stream from r and returns raw metadata payloads.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("png: seek: %w", err)
	}

	var sig [8]byte
	if _, err = io.ReadFull(r, sig[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("png: read signature: %w", err)
	}
	if sig != pngSig {
		return nil, nil, nil, errors.New("png: invalid signature")
	}

	for {
		chunkType, data, rerr := readChunk(r)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, nil, nil, rerr
		}
		var done bool
		rawEXIF, rawXMP, done, err = processExtractChunk(chunkType, data, rawEXIF, rawXMP)
		if err != nil {
			return nil, nil, nil, err
		}
		if done {
			return rawEXIF, nil, rawXMP, nil
		}
	}
	return rawEXIF, nil, rawXMP, nil
}

// handleITXtXMP extracts XMP from an iTXt chunk. Returns (xmp, nil) on
// success, (nil, nil) when the chunk is not an XMP iTXt, or (nil, err).
func handleITXtXMP(data []byte) ([]byte, error) {
	return extractXMPFromITXt(data)
}

// handleLegacyXMP extracts XMP from a tEXt or zTXt chunk only when existing
// is nil (legacy chunks do not override a higher-priority iTXt source).
func handleLegacyXMP(chunkType string, data []byte) ([]byte, error) {
	if chunkType == "zTXt" {
		return extractXMPFromZTxt(data)
	}
	return extractXMPFromTExt(data), nil
}

// handleXMPChunk dispatches iTXt, tEXt, and zTXt chunks to the appropriate
// XMP extractor. It returns existing unchanged if the chunk does not contain
// XMP, or if existing is already set and the chunk type does not override it.
func handleXMPChunk(chunkType string, data []byte, existing []byte) ([]byte, error) {
	switch chunkType {
	case "iTXt":
		xmp, err := handleITXtXMP(data)
		if err != nil {
			return nil, err
		}
		if xmp != nil {
			return xmp, nil
		}
	case "tEXt", "zTXt":
		// Legacy text chunks are only used when no higher-priority XMP was found.
		if existing == nil {
			xmp, err := handleLegacyXMP(chunkType, data)
			if err != nil {
				return nil, err
			}
			if xmp != nil {
				return xmp, nil
			}
		}
	}
	return existing, nil
}

// shouldDropChunk reports whether the chunk should be dropped during Inject
// because it is being replaced by a new metadata chunk. eXIf is always
// dropped; iTXt is dropped only when it carries an XMP payload.
func shouldDropChunk(chunkType string, data []byte) bool {
	if chunkType == "eXIf" {
		return true
	}
	return chunkType == "iTXt" && isXMPChunk(data)
}

// writeInjectChunk writes chunkType/data to w and, if chunkType is "IHDR",
// immediately writes the new metadata chunks. It returns (done=true) when
// chunkType is "IEND". This helper extracts the per-chunk logic from Inject,
// reducing that function's cyclomatic complexity.
func writeInjectChunk(w io.Writer, chunkType string, data, rawEXIF, rawXMP []byte) (done bool, err error) {
	if err = writeChunk(w, chunkType, data); err != nil {
		return false, err
	}
	if chunkType == "IHDR" {
		if err = writeMetadataAfterIHDR(w, rawEXIF, rawXMP); err != nil {
			return false, err
		}
	}
	return chunkType == "IEND", nil
}

// Inject reads the PNG chunk stream from r, replaces or inserts the eXIf and
// iTXt(XMP) chunks, and writes the result to w. IPTC is not natively
// supported in PNG; rawIPTC is ignored.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("png: seek: %w", err)
	}

	// Write PNG signature.
	if _, err := w.Write(pngSig[:]); err != nil {
		return fmt.Errorf("png: write signature: %w", err)
	}
	// Skip signature in r.
	if _, err := io.ReadFull(r, make([]byte, 8)); err != nil {
		return fmt.Errorf("png: read signature: %w", err)
	}

	for {
		chunkType, data, err := readChunk(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		// Skip chunks we are replacing.
		if shouldDropChunk(chunkType, data) {
			continue
		}

		done, err := writeInjectChunk(w, chunkType, data, rawEXIF, rawXMP)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	return nil
}

// writeMetadataAfterIHDR writes the eXIf chunk (if rawEXIF is non-nil) and
// the iTXt XMP chunk (if rawXMP is non-nil) to w. Both chunks are placed
// immediately after IHDR per the PNG metadata extension specification.
func writeMetadataAfterIHDR(w io.Writer, rawEXIF, rawXMP []byte) error {
	if rawEXIF != nil {
		if err := writeChunk(w, "eXIf", rawEXIF); err != nil {
			return err
		}
	}
	if rawXMP != nil {
		xmpChunk := buildXMPChunk(rawXMP)
		if err := writeChunk(w, "iTXt", xmpChunk); err != nil {
			return err
		}
	}
	return nil
}

// readChunk reads one PNG chunk and returns its type and data.
// The CRC is consumed but not verified.
func readChunk(r io.Reader) (chunkType string, data []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, fmt.Errorf("png: read chunk data: %w", err)
	}
	length := int(binary.BigEndian.Uint32(hdr[:4]))
	chunkType = string(hdr[4:8])

	if length > 0 {
		data = make([]byte, length)
		if _, err = io.ReadFull(r, data); err != nil {
			return "", nil, fmt.Errorf("png: truncated chunk %q: %w", chunkType, err)
		}
	}
	// Consume CRC (4 bytes) without verifying.
	var crc [4]byte
	if _, err = io.ReadFull(r, crc[:]); err != nil {
		return "", nil, fmt.Errorf("png: read CRC for %q: %w", chunkType, err)
	}
	return chunkType, data, nil
}

// writeChunk writes a PNG chunk with a correct CRC-32 checksum (PNG §5.4).
func writeChunk(w io.Writer, chunkType string, data []byte) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data))) //nolint:gosec // G115: chunk data length bounded by input
	copy(hdr[4:8], chunkType)
	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("png: write chunk header: %w", err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("png: write chunk data: %w", err)
		}
	}
	// CRC covers chunk type + chunk data (PNG §5.4).
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType)) //nolint:gosec // G104: hash.Hash.Write never returns an error
	_, _ = h.Write(data)              //nolint:gosec // G104: hash.Hash.Write never returns an error
	var crcB [4]byte
	binary.BigEndian.PutUint32(crcB[:], h.Sum32())
	if _, err := w.Write(crcB[:]); err != nil {
		return fmt.Errorf("png: write chunk CRC: %w", err)
	}
	return nil
}

// extractXMPFromITXt parses an iTXt chunk and returns the XMP text if the
// keyword is "XML:com.adobe.xmp", or nil otherwise.
// Compressed iTXt payloads (compFlag != 0) are decompressed via zlib (PNG §11.3.4).
func extractXMPFromITXt(data []byte) ([]byte, error) {
	// iTXt layout: keyword\x00 compFlag(1) compMethod(1) lang\x00 transKw\x00 text
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return nil, nil
	}
	if string(data[:null]) != xmpKeyword {
		return nil, nil
	}
	pos := null + 1 // skip null terminator
	if pos+2 > len(data) {
		return nil, nil
	}
	compFlag := data[pos]
	compMethod := data[pos+1]
	pos += 2 // skip compFlag + compMethod

	// Skip language tag (null-terminated).
	lang := bytes.IndexByte(data[pos:], 0x00)
	if lang < 0 {
		return nil, nil
	}
	pos += lang + 1

	// Skip translated keyword (null-terminated).
	tk := bytes.IndexByte(data[pos:], 0x00)
	if tk < 0 {
		return nil, nil
	}
	pos += tk + 1

	text := data[pos:]

	if compFlag == 0 {
		return text, nil
	}

	// Compressed iTXt: decompress using zlib (compMethod 0 = deflate, PNG §11.3.4).
	if compMethod != 0 {
		return nil, fmt.Errorf("png: compressed XMP: unsupported compression method %d", compMethod)
	}
	dec, err := zlibDecompress(text)
	if err != nil {
		return nil, fmt.Errorf("png: compressed XMP: decompression failed: %w", err)
	}
	return dec, nil
}

// extractXMPFromTExt extracts XMP from a tEXt chunk if its keyword is
// "XML:com.adobe.xmp" (legacy uncompressed form, RFC 2083 §12.13).
func extractXMPFromTExt(data []byte) []byte {
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return nil
	}
	if string(data[:null]) != xmpKeyword {
		return nil
	}
	text := data[null+1:]
	if len(text) == 0 {
		return nil
	}
	return text
}

// extractXMPFromZTxt extracts and decompresses XMP from a zTXt chunk if its
// keyword is "XML:com.adobe.xmp" (legacy deflate-compressed form, PNG §11.3.3).
func extractXMPFromZTxt(data []byte) ([]byte, error) {
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return nil, nil
	}
	if string(data[:null]) != xmpKeyword {
		return nil, nil
	}
	pos := null + 1
	if pos >= len(data) {
		return nil, nil
	}
	compMethod := data[pos]
	pos++
	if compMethod != 0 {
		return nil, fmt.Errorf("png: zTXt XMP: unsupported compression method %d", compMethod)
	}
	dec, err := zlibDecompress(data[pos:])
	if err != nil {
		return nil, fmt.Errorf("png: zTXt XMP: decompression failed: %w", err)
	}
	return dec, nil
}

// isXMPChunk reports whether an iTXt chunk contains XMP data.
func isXMPChunk(data []byte) bool {
	return len(data) > len(xmpKeyword) &&
		bytes.HasPrefix(data, []byte(xmpKeyword)) &&
		data[len(xmpKeyword)] == 0x00
}

// buildXMPChunk constructs an iTXt chunk payload for an XMP packet.
func buildXMPChunk(xmpData []byte) []byte {
	// keyword\x00 compFlag(0) compMethod(0) lang\x00 transKw\x00 text
	var buf bytes.Buffer
	buf.WriteString(xmpKeyword)
	buf.WriteByte(0x00) // null terminator for keyword
	buf.WriteByte(0x00) // compression flag: not compressed
	buf.WriteByte(0x00) // compression method
	buf.WriteByte(0x00) // empty language tag
	buf.WriteByte(0x00) // empty translated keyword
	buf.Write(xmpData)
	return buf.Bytes()
}
