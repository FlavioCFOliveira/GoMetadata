package png

// Adapted from bep/imagemeta imagedecoder_png.go:decodePNG,decompressZTXt, Copyright (c) 2022 Bjørn Erik Pedersen, SPDX-License-Identifier: MIT

import (
	"bytes"

	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
)

// Raw profile chunks. ImageMagick, and tools that follow it such as ExifTool,
// store metadata that PNG has no native chunk for in "Raw profile type <name>"
// text chunks ("Raw profile type iptc", "Raw profile type APP1", "Raw profile
// type exif"). This file reads them (task #302). The text of such a chunk is
//
//	"\n" name "\n" length "\n" hex...
//
// where name is a profile label ("IPTC profile", "exif"), length is the
// decimal byte count of the profile, and the hex digits (any whitespace
// between them is ignored; ImageMagick wraps them at 72 per line) encode the
// profile bytes. The chunk may be tEXt, zTXt or iTXt.
//
// Compared with the reference (bep/imagemeta, zTXt only, no length check):
// tEXt and iTXt are handled as well, the declared length must equal the
// decoded length, malformed hex or a mismatch yields no data (never a panic or
// a partial profile), and the IPTC payload may be a Photoshop IRB, an IRB
// with the "Photoshop 3.0" identifier, or a bare IIM stream.

// rawProfilePrefix is the keyword prefix shared by all raw profile chunks.
const rawProfilePrefix = "Raw profile type "

// Raw profile kinds, named after the keyword suffix (ASCII case-insensitive).
type rawProfileKind uint8

const (
	rawProfileNone rawProfileKind = iota
	rawProfileIPTC
	rawProfileAPP1
	rawProfileEXIF
)

// rawProfileKindOf maps the keyword suffix after rawProfilePrefix to a kind.
func rawProfileKindOf(suffix []byte) rawProfileKind {
	switch {
	case bytes.EqualFold(suffix, []byte("iptc")):
		return rawProfileIPTC
	case bytes.EqualFold(suffix, []byte("APP1")):
		return rawProfileAPP1
	case bytes.EqualFold(suffix, []byte("exif")):
		return rawProfileEXIF
	}
	return rawProfileNone
}

// isRawProfileKeyword reports whether data, the payload of a tEXt, zTXt or
// iTXt chunk, starts with a "Raw profile type <kind>" keyword for a kind this
// package reads. It allocates nothing and is the only cost every other text
// chunk pays.
func isRawProfileKeyword(data []byte) (kind rawProfileKind, keywordLen int) {
	if len(data) < len(rawProfilePrefix) || string(data[:len(rawProfilePrefix)]) != rawProfilePrefix {
		return rawProfileNone, 0
	}
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return rawProfileNone, 0
	}
	return rawProfileKindOf(data[len(rawProfilePrefix):null]), null
}

// rawProfileText returns the text of a text chunk whose keyword occupies
// data[:keywordLen] (PNG §11.3.4: tEXt, zTXt, iTXt layouts), decompressing it
// when the chunk is compressed. ok is false for a malformed chunk.
func rawProfileText(chunkType [4]byte, data []byte, keywordLen int) (text []byte, ok bool) {
	pos := keywordLen + 1 // past the keyword NUL
	switch chunkType {
	case chunkTEXt:
		return data[pos:], true
	case chunkZTXt:
		// compression method byte, then the zlib stream.
		if pos >= len(data) || data[pos] != 0 {
			return nil, false
		}
		dec, err := zlibDecompress(data[pos+1:])
		return dec, err == nil
	case chunkITXt:
		return iTXtText(data, pos)
	}
	return nil, false
}

// iTXtText returns the text of an iTXt chunk whose compression flag starts at
// data[pos] (PNG §11.3.4.5: compression flag, compression method, language
// tag NUL, translated keyword NUL, text).
func iTXtText(data []byte, pos int) (text []byte, ok bool) {
	if pos+2 > len(data) {
		return nil, false
	}
	compFlag, compMethod := data[pos], data[pos+1]
	pos += 2
	for range 2 { // skip language tag and translated keyword
		nul := bytes.IndexByte(data[pos:], 0x00)
		if nul < 0 {
			return nil, false
		}
		pos += nul + 1
	}
	if compFlag == 0 {
		return data[pos:], true
	}
	if compMethod != 0 {
		return nil, false
	}
	dec, err := zlibDecompress(data[pos:])
	return dec, err == nil
}

// decodeRawProfile decodes the text of a raw profile chunk into the profile
// bytes. It returns nil when the header is malformed, the hex payload is
// invalid, or the declared length differs from the decoded length. The output
// is allocated only after the hex digits have been counted, so a huge
// declared length cannot force a large allocation.
func decodeRawProfile(text []byte) []byte {
	declared, hexStart, ok := rawProfileHeader(text)
	if !ok {
		return nil
	}
	return decodeHexDigits(text[hexStart:], declared)
}

// rawProfileHeader parses the header of a raw profile text: optional
// whitespace, the profile name line, optional whitespace, and the decimal
// declared length followed by a line break. It returns the declared length
// and the offset of the line break that precedes the hex digits.
func rawProfileHeader(text []byte) (declared uint64, hexStart int, ok bool) {
	i := skipSpace(text, 0)
	nl := bytes.IndexByte(text[i:], '\n') // end of the profile name line
	if nl < 0 {
		return 0, 0, false
	}
	i = skipSpace(text, i+nl+1)
	digits := 0
	for ; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
		declared = declared*10 + uint64(text[i]-'0')
		if digits++; digits > 10 {
			return 0, 0, false
		}
	}
	if digits == 0 || i >= len(text) || (text[i] != '\n' && text[i] != '\r') {
		return 0, 0, false
	}
	return declared, i, true
}

// decodeHexDigits decodes the hex digits of hexText, ignoring line breaks,
// spaces and tabs between them. It returns nil when any other byte occurs,
// the digit count is odd, or the decoded length differs from declared.
func decodeHexDigits(hexText []byte, declared uint64) []byte {
	n, ok := countHexDigits(hexText)
	if !ok || n%2 != 0 || uint64(n/2) != declared { //nolint:gosec // G115: n counts bytes of hexText, so it is non-negative
		return nil
	}
	out := make([]byte, n/2)
	o := 0
	var hi byte
	half := false
	for _, c := range hexText {
		if !isHexDigit(c) {
			continue
		}
		if !half {
			hi, half = hexValue(c), true
			continue
		}
		out[o] = hi<<4 | hexValue(c)
		o++
		half = false
	}
	return out
}

// countHexDigits returns the number of hex digits in hexText, and false when
// it holds a byte that is neither a hex digit nor a line break, space or tab.
func countHexDigits(hexText []byte) (int, bool) {
	n := 0
	for _, c := range hexText {
		switch {
		case isHexDigit(c):
			n++
		case c == '\n' || c == '\r' || c == ' ' || c == '\t':
		default:
			return 0, false
		}
	}
	return n, true
}

// hexValue returns the value of the hex digit c (validated by isHexDigit).
func hexValue(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == '\n' || b[i] == '\r' || b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}

var (
	identPhotoshop = []byte("Photoshop 3.0\x00")                //nolint:gochecknoglobals // package-level constant bytes
	identExifJPEG  = []byte("Exif\x00\x00")                     //nolint:gochecknoglobals // package-level constant bytes
	identXMPAPP1   = []byte("http://ns.adobe.com/xap/1.0/\x00") //nolint:gochecknoglobals // package-level constant bytes
)

// profileIPTC extracts the IIM stream from an "iptc" raw profile: a Photoshop
// IRB (optionally preceded by the "Photoshop 3.0" identifier) whose 0x0404
// resource holds the IIM data, or a bare IIM stream (first byte 0x1C, IIM 4.2
// §1.5 tag marker).
func profileIPTC(p []byte) []byte {
	p = bytes.TrimPrefix(p, identPhotoshop)
	switch {
	case bytes.HasPrefix(p, []byte("8BIM")):
		return irb.FindIPTC(p)
	case len(p) > 0 && p[0] == 0x1C:
		return p
	}
	return nil
}

// profileEXIF returns the TIFF stream of an "exif" or "APP1" raw profile: the
// bytes after the "Exif\0\0" identifier, or the profile itself when it
// already starts with a TIFF byte-order mark. An APP1 profile that carries an
// XMP packet instead returns it as xmp.
func profileEXIF(kind rawProfileKind, p []byte) (tiff, xmp []byte) {
	switch {
	case bytes.HasPrefix(p, identExifJPEG):
		return p[len(identExifJPEG):], nil
	case kind == rawProfileAPP1 && bytes.HasPrefix(p, identXMPAPP1):
		return nil, p[len(identXMPAPP1):]
	case len(p) >= 4 && (bytes.HasPrefix(p, []byte("II*\x00")) || bytes.HasPrefix(p, []byte("MM\x00*"))):
		return p, nil
	}
	return nil, nil
}

// extractState accumulates what Extract finds while it scans the chunks.
type extractState struct {
	exif, xmp, iptc []byte
	// Fallbacks from raw profile chunks, used only when the native source
	// (eXIf chunk, XMP iTXt) is absent: PNG 3rd Edition defines eXIf and the
	// XMP iTXt as the authoritative carriers.
	profEXIF, profXMP []byte
}

// addRawProfile decodes a raw profile text chunk and records its content. A
// malformed profile is ignored.
func (st *extractState) addRawProfile(chunkType [4]byte, data []byte) {
	kind, kwLen := isRawProfileKeyword(data)
	if kind == rawProfileNone {
		return
	}
	text, ok := rawProfileText(chunkType, data, kwLen)
	if !ok {
		return
	}
	p := decodeRawProfile(text)
	if p == nil {
		return
	}
	if kind == rawProfileIPTC {
		if st.iptc == nil {
			st.iptc = profileIPTC(p) // p is a fresh buffer: it may be retained
		}
		return
	}
	tiff, xmp := profileEXIF(kind, p)
	if tiff != nil && st.profEXIF == nil {
		st.profEXIF = tiff
	}
	if xmp != nil && st.profXMP == nil {
		st.profXMP = xmp
	}
}

// result returns rawEXIF, rawIPTC and rawXMP, preferring the native chunks.
func (st *extractState) result() (rawEXIF, rawIPTC, rawXMP []byte) {
	rawEXIF, rawXMP = st.exif, st.xmp
	if rawEXIF == nil {
		rawEXIF = st.profEXIF
	}
	if rawXMP == nil {
		rawXMP = st.profXMP
	}
	return rawEXIF, st.iptc, rawXMP
}
