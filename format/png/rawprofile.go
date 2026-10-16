package png

// Adapted from bep/imagemeta imagedecoder_png.go:decodePNG,decompressZTXt, Copyright (c) 2022 Bjørn Erik Pedersen, SPDX-License-Identifier: MIT

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strconv"

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
	rawProfileXMP
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
	case bytes.EqualFold(suffix, []byte("xmp")):
		return rawProfileXMP
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

// maxRawProfileBudget bounds the text that all the "Raw profile type ..."
// chunks of one PNG may inflate to, in one Extract or one Inject. A profile is
// the hex text of an IPTC, Exif or XMP block: a few kilobytes, and 2 bytes of
// text per byte of an XMP packet of at most 16 MiB (the xmp package's own
// limit), so 32 MiB is far above any real file. Without the bound every such
// chunk is inflated up to 64 MiB on its own (security audit F-08: 40 chunks
// cost 2.1 s of Read), and each chunk of the same kind after the first would be
// inflated for nothing. Reference: Exiv2 pngimage.cpp inflates a chunk only
// when its keyword is one it reads, and stops at the first of each kind.
const maxRawProfileBudget int64 = 32 << 20

// profileBudget counts the text inflated so far in one Extract or Inject; the
// zero value is a full budget of maxRawProfileBudget.
type profileBudget struct{ used int64 }

// newProfileBudget returns a full budget.
func newProfileBudget() profileBudget { return profileBudget{} }

// limit returns the output cap for the next inflate: what is left of the
// budget, or the per-chunk cap for a nil budget.
func (b *profileBudget) limit() int64 {
	if b == nil {
		return maxZlibDecompressSize
	}
	return maxRawProfileBudget - b.used
}

// spend charges n inflated bytes to the budget.
func (b *profileBudget) spend(n int) {
	if b != nil {
		b.used += int64(n)
	}
}

// inflate decompresses data within the budget. A stream that is not valid zlib
// is (nil, nil): the chunk is malformed and ignored, as it always was; only the
// budget running out is an error.
func (b *profileBudget) inflate(data []byte) ([]byte, error) {
	dec, err := zlibDecompressLimit(data, b.limit())
	if err != nil {
		if errors.Is(err, ErrRawProfileBudget) {
			return nil, err
		}
		return nil, nil
	}
	b.spend(len(dec))
	return dec, nil
}

// rawProfileText returns the text of a text chunk whose keyword occupies
// data[:keywordLen] (PNG §11.3.4: tEXt, zTXt, iTXt layouts), decompressing it
// when the chunk is compressed. ok is false for a malformed chunk.
func rawProfileText(chunkType [4]byte, data []byte, keywordLen int) (text []byte, ok bool) {
	text, ok, _ = rawProfileTextB(chunkType, data, keywordLen, nil)
	return text, ok
}

// rawProfileTextB is rawProfileText with the inflate charged to budget; err is
// ErrRawProfileBudget when it runs out.
func rawProfileTextB(chunkType [4]byte, data []byte, keywordLen int, budget *profileBudget) (text []byte, ok bool, err error) {
	pos := keywordLen + 1 // past the keyword NUL
	switch chunkType {
	case chunkTEXt:
		return data[pos:], true, nil
	case chunkZTXt:
		// compression method byte, then the zlib stream.
		if pos >= len(data) || data[pos] != 0 {
			return nil, false, nil
		}
		dec, err := budget.inflate(data[pos+1:])
		return dec, dec != nil, err
	case chunkITXt:
		return iTXtTextB(data, pos, budget)
	}
	return nil, false, nil
}

// iTXtTextB returns the text of an iTXt chunk whose compression flag starts at
// data[pos] (PNG §11.3.4.5: compression flag, compression method, language
// tag NUL, translated keyword NUL, text), inflating it within budget.
func iTXtTextB(data []byte, pos int, budget *profileBudget) (text []byte, ok bool, err error) {
	compFlag, compMethod, pos, ok := iTXtTextStart(data, pos)
	if !ok {
		return nil, false, nil
	}
	if compFlag == 0 {
		return data[pos:], true, nil
	}
	if compMethod != 0 {
		return nil, false, nil
	}
	dec, err := budget.inflate(data[pos:])
	return dec, dec != nil, err
}

// iTXtTextStart returns the compression flag and method of an iTXt chunk
// whose compression flag is at data[pos], and the offset of its text (after
// the language tag and translated keyword, PNG §11.3.4.5).
func iTXtTextStart(data []byte, pos int) (compFlag, compMethod byte, textStart int, ok bool) {
	if pos+2 > len(data) {
		return 0, 0, 0, false
	}
	compFlag, compMethod = data[pos], data[pos+1]
	pos += 2
	for range 2 { // skip language tag and translated keyword
		nul := bytes.IndexByte(data[pos:], 0x00)
		if nul < 0 {
			return 0, 0, 0, false
		}
		pos += nul + 1
	}
	return compFlag, compMethod, pos, true
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
// XMP packet instead returns it as xmp, and an "xmp" profile is a bare XMP
// packet (ExifTool PNG.pm, 'Raw profile type xmp'; task #325).
func profileEXIF(kind rawProfileKind, p []byte) (tiff, xmp []byte) {
	switch {
	case kind == rawProfileXMP:
		// A UTF-8 byte order mark may precede the packet (ImageMagick and
		// .NET writers emit one); it is not part of the XMP text, and PNG
		// iTXt text is plain UTF-8.
		p = bytes.TrimPrefix(p, []byte("\xEF\xBB\xBF"))
		if t := bytes.TrimLeft(p, " \t\r\n"); len(t) > 0 && t[0] == '<' {
			return nil, p
		}
		return nil, nil
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
	// budget bounds the text that the raw profile chunks inflate to.
	budget profileBudget
}

// wants reports whether a raw profile of the given kind can still add
// anything to st: the first profile of each kind wins, so once its slot is
// filled the later chunks of that kind are not inflated at all (the invariant
// of at most one decompression per kind that v1.4.0 had; security audit F-08).
// An APP1 profile holds Exif or XMP, so it is wanted while either is missing.
func (st *extractState) wants(kind rawProfileKind) bool {
	switch kind {
	case rawProfileIPTC:
		return st.iptc == nil
	case rawProfileEXIF:
		return st.profEXIF == nil
	case rawProfileXMP:
		return st.profXMP == nil
	case rawProfileAPP1:
		return st.profEXIF == nil || st.profXMP == nil
	case rawProfileNone:
	}
	return false
}

// addRawProfile decodes a raw profile text chunk and records its content. A
// malformed profile is ignored, and so is a profile of a kind that an earlier
// chunk already supplied. The error is ErrRawProfileBudget when the chunks
// together inflate to more than maxRawProfileBudget.
func (st *extractState) addRawProfile(chunkType [4]byte, data []byte) error {
	kind, kwLen := isRawProfileKeyword(data)
	if kind == rawProfileNone || !st.wants(kind) {
		return nil
	}
	text, ok, err := rawProfileTextB(chunkType, data, kwLen, &st.budget)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if p := decodeRawProfile(text); p != nil {
		st.record(kind, p)
	}
	return nil
}

// record stores the decoded profile p of the given kind in the first free slot
// of its kind.
func (st *extractState) record(kind rawProfileKind, p []byte) {
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

// Writing raw profiles (task #314). After an IPTC write, an existing
// "Raw profile type iptc" chunk would otherwise keep the old IIM and
// contradict the IPTC just written (ExifTool, which reads and writes this
// chunk, rewrites it in place). rewriteRawProfileIPTC re-encodes such a chunk
// with the new IIM: same chunk type, keyword, compression and iTXt language
// fields, same profile name line, and the payload form of the original (a
// Photoshop IRB, with or without the "Photoshop 3.0" identifier, spliced by
// irb.AppendSpliced so every other resource is kept; or a bare IIM stream).
// The text layout is the one ImageMagick defines and ExifTool writes:
//
//	"\n" name "\n" length (decimal, right-aligned in 8 columns) "\n"
//	hex digits, 72 per line, each line ended by "\n"

// rawProfileHexLine is the number of profile bytes per hex line (72 digits).
const rawProfileHexLine = 36

// rewriteRawProfileIPTC returns the payload that replaces data, the payload of
// a text chunk of type chunkType, so that its IPTC raw profile holds newIIM;
// ok is false when data must be kept as is: it is not an IPTC raw profile, it
// cannot be decoded (a malformed chunk is preserved, never repaired), its
// payload form is not one profileIPTC reads, or it would not change. withXMP
// is true when the PNG carries XMP as well: a profile in the Photoshop IRB form
// then also gets the IPTC digest (resource 0x0425) of newIIM, created or
// refreshed (irb.AppendSpliced; MWG Guidelines v2.0 §4.2.3.2, p. 29), even
// when the IIM itself is unchanged.
func rewriteRawProfileIPTC(chunkType [4]byte, data, newIIM []byte, withXMP bool) (payload []byte, ok bool, err error) {
	return rewriteRawProfileIPTCB(chunkType, data, newIIM, withXMP, nil)
}

// rewriteRawProfileIPTCB is rewriteRawProfileIPTC with the inflate charged to
// budget; err is ErrRawProfileBudget when it runs out.
func rewriteRawProfileIPTCB(chunkType [4]byte, data, newIIM []byte, withXMP bool, budget *profileBudget) (payload []byte, ok bool, err error) {
	kind, kwLen := isRawProfileKeyword(data)
	if kind != rawProfileIPTC {
		return nil, false, nil
	}
	text, tok, terr := rawProfileTextB(chunkType, data, kwLen, budget)
	if terr != nil {
		return nil, false, terr
	}
	if !tok {
		return nil, false, nil
	}
	p := decodeRawProfile(text)
	cur := profileIPTC(p)
	if cur == nil || (bytes.Equal(cur, newIIM) && !withXMP) {
		return nil, false, nil
	}
	newProfile := splicedProfile(p, newIIM, withXMP)
	if bytes.Equal(newProfile, p) {
		return nil, false, nil
	}
	newText := appendRawProfileText(nil, text, newProfile)
	return rawProfileChunk(chunkType, data, kwLen, newText)
}

// splicedProfile returns profile p (a form profileIPTC reads) with its IIM
// replaced by newIIM and, in the Photoshop IRB form, its IPTC digest set as
// irb.AppendSpliced does for withXMP.
func splicedProfile(p, newIIM []byte, withXMP bool) []byte {
	body := bytes.TrimPrefix(p, identPhotoshop)
	if !bytes.HasPrefix(body, []byte("8BIM")) {
		return newIIM // bare IIM stream
	}
	ident := p[:len(p)-len(body)]
	out := make([]byte, 0, len(p)+len(newIIM))
	out = append(out, ident...)
	return irb.AppendSpliced(out, body, newIIM, withXMP)
}

// appendRawProfileText appends to dst the raw profile text of profile, keeping
// the leading whitespace and the profile name line of the original text old
// (validated by rawProfileHeader).
func appendRawProfileText(dst, old, profile []byte) []byte {
	i := skipSpace(old, 0)
	nameEnd := i + bytes.IndexByte(old[i:], '\n') + 1
	dst = append(dst, old[:nameEnd]...)
	n := strconv.Itoa(len(profile))
	for range 8 - min(8, len(n)) {
		dst = append(dst, ' ')
	}
	dst = append(dst, n...)
	dst = append(dst, '\n')
	const digits = "0123456789abcdef"
	for j, c := range profile {
		if j > 0 && j%rawProfileHexLine == 0 {
			dst = append(dst, '\n')
		}
		dst = append(dst, digits[c>>4], digits[c&0x0F])
	}
	return append(dst, '\n')
}

// rawProfileChunk rebuilds the payload of the text chunk data (keyword of
// kwLen bytes) around newText, keeping its compression settings: tEXt text
// as is; zTXt compression method 0 and a zlib stream; iTXt flag, method,
// language tag and translated keyword as in data, the text compressed when
// the flag is set (PNG §11.3.4.3–5).
func rawProfileChunk(chunkType [4]byte, data []byte, kwLen int, newText []byte) ([]byte, bool, error) {
	var head []byte
	compress := false
	switch chunkType {
	case chunkTEXt:
		head = data[:kwLen+1]
	case chunkZTXt:
		head, compress = data[:kwLen+2], true
	case chunkITXt:
		flag, _, textStart, ok := iTXtTextStart(data, kwLen+1)
		if !ok {
			return nil, false, nil
		}
		head, compress = data[:textStart], flag != 0
	default:
		return nil, false, nil
	}
	out := make([]byte, 0, len(head)+len(newText))
	out = append(out, head...)
	if !compress {
		return append(out, newText...), true, nil
	}
	var buf bytes.Buffer
	buf.Write(out)
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(newText); err != nil {
		return nil, false, fmt.Errorf("png: compress raw profile: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, false, fmt.Errorf("png: compress raw profile: %w", err)
	}
	return buf.Bytes(), true, nil
}

// Superseded raw profiles (task #325). Inject writes EXIF to the eXIf chunk
// and XMP to the "XML:com.adobe.xmp" iTXt chunk. An EXIF, APP1 or XMP raw
// profile that Extract read as the source of that content is a second copy of
// it once the standard chunk is written, and would contradict it after an
// edit: PNG 3rd Edition §11.3.4.5 allows only one eXIf chunk, and XMP
// Specification Part 3 §1.1.5 (p. 16) asks for no more than one chunk
// containing XMP. The copy is dropped instead of being rewritten in place
// because the standard chunk is the authoritative carrier and a rewritten
// copy would only keep two values to keep in step (ExifTool, PNG.pm, updates
// the non-standard copies in place and writes the standard chunk as well; it
// is a study reference only).

// rawProfileSuperseded reports whether the text chunk data (type chunkType) is
// an EXIF, APP1 or XMP raw profile whose content is replaced by a standard
// chunk Inject writes: haveEXIF and haveXMP tell which standard chunks are
// written. A profile that cannot be decoded, or whose payload form is not one
// this package reads, is never dropped: its content has not been carried
// over, so it is preserved byte-identical.
func rawProfileSuperseded(chunkType [4]byte, data []byte, haveEXIF, haveXMP bool) bool {
	superseded, _ := rawProfileSupersededB(chunkType, data, haveEXIF, haveXMP, nil)
	return superseded
}

// rawProfileSupersededB is rawProfileSuperseded with the inflate charged to
// budget; err is ErrRawProfileBudget when it runs out.
func rawProfileSupersededB(chunkType [4]byte, data []byte, haveEXIF, haveXMP bool, budget *profileBudget) (bool, error) {
	if !haveEXIF && !haveXMP {
		return false, nil
	}
	kind, kwLen := isRawProfileKeyword(data)
	if kind == rawProfileNone || kind == rawProfileIPTC {
		return false, nil
	}
	p, err := decodedProfile(chunkType, data, kwLen, budget)
	if p == nil || err != nil {
		return false, err
	}
	tiff, xmp := profileEXIF(kind, p)
	return (tiff != nil && haveEXIF) || (xmp != nil && haveXMP), nil
}

// decodedProfile returns the profile bytes of the raw profile chunk data, or
// nil when the chunk is malformed; err is ErrRawProfileBudget when the inflate
// runs out of budget.
func decodedProfile(chunkType [4]byte, data []byte, kwLen int, budget *profileBudget) ([]byte, error) {
	text, ok, err := rawProfileTextB(chunkType, data, kwLen, budget)
	if err != nil || !ok {
		return nil, err
	}
	return decodeRawProfile(text), nil
}
