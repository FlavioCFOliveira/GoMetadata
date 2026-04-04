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
		zr = v.(io.ReadCloser)
		if err := zr.(zlib.Resetter).Reset(r, nil); err != nil {
			return nil, err
		}
	} else {
		var err error
		zr, err = zlib.NewReader(r)
		if err != nil {
			return nil, err
		}
	}
	// Return to pool without closing so it can be Reset on next use.
	defer zlibPool.Put(zr)
	return io.ReadAll(zr)
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
		return nil, nil, nil, fmt.Errorf("png: invalid signature")
	}

	for {
		chunkType, data, rerr := readChunk(r)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, nil, nil, rerr
		}
		switch chunkType {
		case "eXIf":
			rawEXIF = data
		case "iTXt":
			xmp, xerr := extractXMPFromITXt(data)
			if xerr != nil {
				return nil, nil, nil, xerr
			}
			if xmp != nil {
				rawXMP = xmp
			}
		case "tEXt":
			// Legacy uncompressed text chunk; XMP may be embedded here by older
			// tools (e.g. Photoshop CS2, ImageMagick). Layout: keyword\x00text.
			// Only extract if keyword is "XML:com.adobe.xmp" and rawXMP not yet set.
			if rawXMP == nil {
				if xmp := extractXMPFromTExt(data); xmp != nil {
					rawXMP = xmp
				}
			}
		case "zTXt":
			// Legacy compressed text chunk; same keyword convention as tEXt.
			// Layout: keyword\x00 compMethod(1) compressed_text. Only deflate (0).
			if rawXMP == nil {
				xmp, xerr := extractXMPFromZTxt(data)
				if xerr != nil {
					return nil, nil, nil, xerr
				}
				if xmp != nil {
					rawXMP = xmp
				}
			}
		case "IEND":
			return rawEXIF, nil, rawXMP, nil
		}
	}
	return rawEXIF, nil, rawXMP, nil
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
		return err
	}
	// Skip signature in r.
	if _, err := io.ReadFull(r, make([]byte, 8)); err != nil {
		return fmt.Errorf("png: read signature: %w", err)
	}

	// Write new metadata chunks right after IHDR.
	ihdrWritten := false

	for {
		chunkType, data, err := readChunk(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		// Skip chunks we are replacing.
		if chunkType == "eXIf" {
			continue
		}
		if chunkType == "iTXt" && isXMPChunk(data) {
			continue
		}

		// Write this chunk.
		if err := writeChunk(w, chunkType, data); err != nil {
			return err
		}

		// After writing IHDR, insert our metadata chunks.
		if chunkType == "IHDR" && !ihdrWritten {
			ihdrWritten = true
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
		}

		if chunkType == "IEND" {
			break
		}
	}
	return nil
}

// readChunk reads one PNG chunk and returns its type and data.
// The CRC is consumed but not verified.
func readChunk(r io.Reader) (chunkType string, data []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, err
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
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data)))
	copy(hdr[4:8], chunkType)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	// CRC covers chunk type + chunk data (PNG §5.4).
	h := crc32.NewIEEE()
	h.Write([]byte(chunkType))
	h.Write(data)
	var crcB [4]byte
	binary.BigEndian.PutUint32(crcB[:], h.Sum32())
	_, err := w.Write(crcB[:])
	return err
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
