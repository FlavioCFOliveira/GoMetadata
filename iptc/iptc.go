// Package iptc implements an IPTC IIM parser and writer.
//
// Compliance: IPTC IIM version 4.2 (IPTC-NAA Information Interchange Model).
// Spec citations reference the IIM document as "IIM §<section>".
//
// This package operates on the raw IIM byte stream. Extraction of that stream
// from container-specific envelopes (e.g., Photoshop IRB inside JPEG APP13)
// is the responsibility of the container layer (format/jpeg).
package iptc

import (
	"bytes"
	"sync"
	"unicode/utf8"
)

// IPTC holds the parsed IPTC datasets grouped by record number.
type IPTC struct {
	// Records holds datasets indexed by record number (0–9).
	// IIM defines records 1–9; index 0 is a pseudo-record used internally
	// to store the UTF-8 flag (see isUTF8). Using a fixed-size array instead
	// of a map eliminates the map allocation entirely — one fewer heap object
	// per Parse call — and allows O(1) index access without hashing.
	Records [10][]Dataset

	// Truncated is set to true when Parse encountered one or more datasets
	// that were skipped because of a recoverable anomaly (oversized individual
	// dataset, declared length exceeding available bytes). It does NOT indicate
	// that valid datasets were lost — only that some datasets were skipped.
	//
	// Parse always returns err == nil regardless of this field; the nil-error
	// contract allows callers (e.g. read.go) to use recovered datasets without
	// treating a partial skip as a fatal failure. Callers that care about data
	// completeness can inspect this field. The DoS guard (aggregate byte cap)
	// is the only condition that terminates parsing entirely.
	Truncated bool
}

// Dataset is a single IPTC record:dataset value (IIM §1.6).
type Dataset struct {
	Record  uint8
	DataSet uint8
	Value   []byte
	// decodedValue and decoded implement a one-shot charset decode cache so
	// that callers that read the same field repeatedly (e.g. Keywords in a
	// loop) pay the ISO-8859-1 → UTF-8 conversion cost only once.
	decodedValue string
	decoded      bool
}

// decodeDatasetLength decodes the 2-byte size field starting at b[pos+3..pos+4]
// (IIM §1.6.2). If bit 15 of the size field is set, the length is encoded in
// the next (sizeHigh & 0x7F) bytes (extended form); otherwise the two bytes
// themselves are the length (standard form). pos must already point to the
// tag marker (0x1C); the caller is expected to have verified pos+5 ≤ len(b).
// Returns (length, newPos, true) on success or (0, pos, false) on bounds
// violation inside the extended block.
func decodeDatasetLength(b []byte, pos int) (length, newPos int, ok bool) {
	sizeHigh := b[pos+3]
	sizeLow := b[pos+4]
	newPos = pos + 5

	if sizeHigh&0x80 != 0 {
		// Extended length encoding (IIM §1.6.2): bit 15 set; lower 15 bits
		// carry the byte count for the actual length value.
		nBytes := int(sizeHigh&0x7F)<<8 | int(sizeLow)
		if nBytes < 1 || nBytes > 4 || newPos+nBytes > len(b) {
			return 0, pos, false
		}
		for j := range nBytes {
			length = length<<8 | int(b[newPos+j])
		}
		// Guard against sign-bit overflow on 32-bit platforms (IIM §1.6.2).
		if length < 0 {
			return 0, pos, false
		}
		newPos += nBytes
	} else {
		// Standard length encoding (IIM §1.6.2): the two size bytes are the length.
		length = int(sizeHigh)<<8 | int(sizeLow)
	}

	return length, newPos, true
}

// storeDataset handles the UTF-8 marker detection (record 1, dataset 90;
// IIM §1.5.1) and appends the dataset to i.Records[record] for all other
// datasets. The utf8 pointer is updated in-place when the marker is found.
func storeDataset(i *IPTC, record, dataset uint8, value []byte, utf8 *bool) {
	// IIM §1.6: valid record numbers are 1–9. Record 0 is an internal
	// pseudo-record (UTF-8 flag) never present on the wire. Any record byte
	// outside [1, len(i.Records)-1] is out of range for the fixed-size array
	// and must be skipped; the caller continues scanning so later valid
	// datasets in the same stream are not lost.
	if record < 1 || int(record) >= len(i.Records) {
		return
	}
	// Record 1, dataset 90 (1:90) carries the coded character set declaration
	// (IIM §1.5.1). ESC % G signals UTF-8.
	if record == 1 && dataset == 90 {
		*utf8 = isUTF8Declaration(value)
		return
	}
	// We store datasets as raw bytes; charset decoding happens on access
	// (see firstRecord2 / stringValue).
	i.Records[record] = append(i.Records[record], Dataset{
		Record:  record,
		DataSet: dataset,
		Value:   value,
	})
}

// maxIPTCTotalBytes is the maximum aggregate size of all parsed IPTC dataset
// values. This prevents memory exhaustion from streams with many large datasets.
const maxIPTCTotalBytes = 256 << 20 // 256 MiB

// Parse parses a raw IPTC IIM byte stream.
// b must begin with (or contain) the IPTC tag marker 0x1C (IIM §1.6).
//
// Parse always returns a non-nil *IPTC and a nil error. Individual malformed
// or oversized datasets are skipped (recoverable truncation) and the
// *IPTC.Truncated field is set to true to signal that some datasets were
// omitted. Only the DoS aggregate-bytes guard terminates parsing entirely, but
// even then the datasets collected so far are returned with err == nil.
//
// The nil-error contract is intentional: callers such as read.go treat a
// non-nil error as a fatal segment failure and discard all recovered data.
// Callers that need to detect partial skips should inspect IPTC.Truncated.
func Parse(b []byte) (*IPTC, error) {
	i := new(IPTC)
	// Pre-allocate record 2 (Application Record) — the most common record,
	// typically containing 5–15 datasets in a production JPEG (IIM §2).
	i.Records[2] = make([]Dataset, 0, 12)
	utf8 := false
	totalBytes := 0

	pos := 0
	for pos < len(b) {
		// Scan forward to the next tag marker 0x1C (IIM §1.6).
		if b[pos] != 0x1C {
			pos++
			continue
		}

		// Need at least 5 bytes: marker(1) + record(1) + dataset(1) + size(2).
		if pos+5 > len(b) {
			break
		}

		record := b[pos+1]  //nolint:gosec // G602: bounds guaranteed by the pos+5 guard above (IIM §1.6)
		dataset := b[pos+2] //nolint:gosec // G602: bounds guaranteed by the pos+5 guard above (IIM §1.6)

		length, newPos, ok := decodeDatasetLength(b, pos)
		if !ok {
			// #18: the extended-length block itself is malformed (nBytes out of
			// range or truncated buffer). Skip to byte after the current marker
			// byte and re-scan for the next 0x1C. This is recoverable: the
			// malformed length block occupies a bounded number of bytes and there
			// may be valid datasets after it.
			pos++
			i.Truncated = true
			continue
		}

		// #18: recover from individual dataset anomalies instead of stopping.
		// Each condition below skips only the current dataset and advances pos
		// past the header so the scanner does not re-examine the same 0x1C.
		//   • length > 1 MiB: single dataset DoS guard; skip.
		//   • pos+length > len(b): declared value extends past end of buffer; skip.
		// The aggregate DoS guard (totalBytes > maxIPTCTotalBytes) is the only
		// irrecoverable condition: we stop there to bound total memory use.
		if length > 1<<20 || newPos+length > len(b) {
			// Advance past the header so the loop re-scans from the byte after
			// the current 0x1C, rather than looping on the same marker forever.
			pos = newPos
			i.Truncated = true
			continue
		}

		// Cap aggregate size to prevent memory exhaustion from many large datasets.
		totalBytes += length
		if totalBytes > maxIPTCTotalBytes {
			// Irrecoverable: we cannot bound memory usage without stopping.
			// Return whatever we have collected so far (err == nil preserved).
			i.Truncated = true
			break
		}

		value := b[newPos : newPos+length]
		pos = newPos + length

		storeDataset(i, record, dataset, value, &utf8)
	}

	// Store the UTF-8 flag as a pseudo-dataset in record 0 so convenience
	// methods can retrieve it without re-scanning record 1.
	if utf8 {
		i.Records[0] = append(i.Records[0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}

	return i, nil
}

// hasHighBytes reports whether b contains any byte value > 0x7F.
// Used to determine whether a non-UTF-8-declared stream needs auto-upgrade.
func hasHighBytes(b []byte) bool {
	for _, c := range b {
		if c > 0x7F {
			return true
		}
	}
	return false
}

// needsUTF8Declaration reports whether any dataset in record 2 (or other
// records) contains bytes outside the ASCII range.
func (i *IPTC) needsUTF8Declaration() bool {
	for rec := uint8(1); rec <= 9; rec++ {
		for idx := range i.Records[rec] {
			if hasHighBytes(i.Records[rec][idx].Value) {
				return true
			}
		}
	}
	return false
}

// encBufPool reuses bytes.Buffer allocations across Encode calls. This avoids
// repeated heap allocation of the buffer's internal byte array on every call.
// The result is always a fresh bytes.Clone of the buffer contents, so the
// returned slice is safe to use after the buffer is returned to the pool.
var encBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }} //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// Encode serialises i back to an IPTC IIM byte stream.
//
// #19 — Auto-inject UTF-8 declaration: if the stream does not already carry a
// coded character set declaration (1:90) but one or more dataset values contain
// bytes outside the ASCII range (>0x7F), Encode automatically prepends the
// 1:90 ESC % G declaration so that a reader will interpret the bytes correctly
// as UTF-8. Without this, a receiver that defaults to ISO-8859-1 would
// misinterpret multi-byte UTF-8 sequences (mojibake). The injected declaration
// also updates the internal UTF-8 flag so that an immediate Parse of the result
// returns the correct strings (round-trip correctness).
func Encode(i *IPTC) ([]byte, error) {
	buf := encBufPool.Get().(*bytes.Buffer) //nolint:forcetypeassert,revive // encBufPool.New always stores *bytes.Buffer; pool invariant
	buf.Reset()

	// Determine whether to emit the coded character set declaration (IIM §1.5.1).
	// Emit it when:
	//   (a) the stream was parsed with an existing 1:90 UTF-8 declaration, OR
	//   (b) the stream has no 1:90 declaration but contains non-ASCII bytes.
	// Case (b) is the auto-inject path for #19: writing "café" into a fresh
	// *IPTC (no declaration) must produce a stream that round-trips correctly.
	emitUTF8Decl := i.isUTF8() || i.needsUTF8Declaration()
	if emitUTF8Decl {
		// Record 1, Dataset 90: coded character set = UTF-8 (ESC % G).
		buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47})
		// Ensure the internal flag is set so that if the caller re-reads via
		// the convenience accessors without an intervening Parse, the strings
		// are still decoded correctly (no-parse round-trip path).
		if !i.isUTF8() {
			i.Records[0] = append(i.Records[0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
		}
	}

	// Write records in order for deterministic output.
	for record := uint8(1); record <= 9; record++ {
		for _, ds := range i.Records[record] {
			buf.WriteByte(0x1C)
			buf.WriteByte(ds.Record)
			buf.WriteByte(ds.DataSet)
			n := len(ds.Value)
			if n >= 0x8000 {
				// Extended length encoding (IIM §1.6.2): the 2-byte size field
				// has bit 15 set; the remaining 15 bits encode the byte count
				// for the actual length. We use a 4-byte length (0x0004):
				//   high byte = 0x80 | (4 >> 8) = 0x80
				//   low byte  = 4 & 0xFF        = 0x04
				// followed by the 4-byte big-endian length value.
				buf.WriteByte(0x80)          // bit 15 set; upper 7 bits of count = 0
				buf.WriteByte(0x04)          // lower 8 bits of count = 4
				buf.WriteByte(byte(n >> 24)) //nolint:gosec // G115: intentional byte extraction per IPTC IIM §1.6.2 extended length encoding
				buf.WriteByte(byte(n >> 16)) //nolint:gosec // G115: intentional byte extraction per IPTC IIM §1.6.2 extended length encoding
				buf.WriteByte(byte(n >> 8))  //nolint:gosec // G115: intentional byte extraction per IPTC IIM §1.6.2 extended length encoding
				buf.WriteByte(byte(n))       //nolint:gosec // G115: intentional byte extraction per IPTC IIM §1.6.2 extended length encoding
			} else {
				buf.WriteByte(byte(n >> 8)) // intentional byte extraction per IPTC IIM §1.6.2 standard length encoding
				buf.WriteByte(byte(n))      //nolint:gosec // G115: intentional byte extraction per IPTC IIM §1.6.2 standard length encoding
			}
			buf.Write(ds.Value)
		}
	}
	result := bytes.Clone(buf.Bytes())
	encBufPool.Put(buf)
	return result, nil
}

// truncateToLimit truncates value to at most maxLen bytes without cutting a
// UTF-8 multi-byte rune in half. If maxLen is 0 (no limit defined for this
// dataset), the original slice is returned unchanged.
//
// Policy (#17): the library truncates silently rather than returning an error.
// Rationale: IPTC field limits are a legacy IIM constraint; callers writing
// "Nikon D3500, ISO 800, f/2.8, 1/200s — Coastal Portugal 2024" as a Caption
// should not be required to handle an error for a detail they may not know
// about. Truncation at a rune boundary ensures the output is always valid
// UTF-8. Callers that need strict enforcement should check len(s) against the
// IIM limits before calling setters.
func truncateToLimit(value []byte, maxLen int) []byte {
	if maxLen == 0 || len(value) <= maxLen {
		return value
	}
	// Walk backwards from maxLen to find a valid UTF-8 rune boundary so we do
	// not emit a partial sequence. For ASCII-only content the loop runs once.
	trunc := value[:maxLen]
	for len(trunc) > 0 && !utf8.Valid(trunc) {
		trunc = trunc[:len(trunc)-1]
	}
	return trunc
}

// Copyright returns the value of dataset 2:116 (Copyright Notice, IIM §2.2.28).
func (i *IPTC) Copyright() string {
	return i.firstRecord2(DS2CopyrightNotice)
}

// Caption returns the value of dataset 2:120 (Caption/Abstract, IIM §2.2.29).
func (i *IPTC) Caption() string {
	return i.firstRecord2(DS2Caption)
}

// Keywords returns all Record 2 dataset 2:25 (Keywords, IIM §2.2.17) values.
// Keywords is a repeatable dataset; each occurrence is a separate keyword.
func (i *IPTC) Keywords() []string {
	if i == nil {
		return nil
	}
	utf8flag := i.isUTF8()
	var result []string
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Keywords {
			result = append(result, i.Records[2][idx].stringValue(utf8flag))
		}
	}
	return result
}

// Creator returns the first value of dataset 2:80 (By-line / author, IIM §2.2.25).
// By-line is a repeatable dataset; use AllCreators to retrieve all occurrences.
func (i *IPTC) Creator() string {
	return i.firstRecord2(DS2Byline)
}

// AllCreators returns all values of dataset 2:80 (By-line, IIM §2.2.25).
// By-line is a repeatable dataset; each occurrence represents one creator.
// Returns nil when there are no By-line datasets.
func (i *IPTC) AllCreators() []string {
	if i == nil {
		return nil
	}
	utf8flag := i.isUTF8()
	var result []string
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Byline {
			result = append(result, i.Records[2][idx].stringValue(utf8flag))
		}
	}
	return result
}

// DateCreated returns the value of dataset 2:55 (Date Created, IIM §2.2.23)
// as a string in CCYYMMDD format (e.g. "20240315"). Returns an empty string
// when the dataset is absent.
func (i *IPTC) DateCreated() string {
	return i.firstRecord2(DS2DateCreated)
}

// TimeCreated returns the value of dataset 2:60 (Time Created, IIM §2.2.23)
// as a string in HHMMSS±HHMM format (e.g. "143000+0100"). Returns an empty
// string when the dataset is absent.
func (i *IPTC) TimeCreated() string {
	return i.firstRecord2(DS2TimeCreated)
}

// SetCaption sets dataset 2:120 (Caption/Abstract) to s, replacing any existing value.
// Values exceeding 2000 bytes are truncated at a UTF-8 rune boundary (IIM §2.2.29).
func (i *IPTC) SetCaption(s string) {
	i.setRecord2(DS2Caption, truncateToLimit([]byte(s), datasetMaxLen[DS2Caption]))
}

// SetCopyright sets dataset 2:116 (Copyright Notice) to s, replacing any existing value.
// Values exceeding 128 bytes are truncated at a UTF-8 rune boundary (IIM §2.2.28).
func (i *IPTC) SetCopyright(s string) {
	i.setRecord2(DS2CopyrightNotice, truncateToLimit([]byte(s), datasetMaxLen[DS2CopyrightNotice]))
}

// SetCreator sets the first dataset 2:80 (By-line) to s, replacing any existing
// first occurrence. By-line is repeatable; use AddCreator to append additional
// entries. Values exceeding 32 bytes are truncated at a UTF-8 rune boundary
// (IIM §2.2.25).
func (i *IPTC) SetCreator(s string) {
	i.setRecord2(DS2Byline, truncateToLimit([]byte(s), datasetMaxLen[DS2Byline]))
}

// AddCreator appends a creator to dataset 2:80 (By-line, IIM §2.2.25).
// By-line is a repeatable dataset; each call adds one additional entry.
// Values exceeding 32 bytes are truncated at a UTF-8 rune boundary.
func (i *IPTC) AddCreator(creator string) {
	v := truncateToLimit([]byte(creator), datasetMaxLen[DS2Byline])
	if hasHighBytes(v) && !i.isUTF8() {
		i.Records[0] = append(i.Records[0][:0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}
	i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Byline, Value: v})
}

// AddKeyword appends a keyword to dataset 2:25 (Keywords, IIM §2.2.17).
// Keywords is a repeatable dataset; each call adds one additional entry.
// Values exceeding 64 bytes are truncated at a UTF-8 rune boundary.
func (i *IPTC) AddKeyword(kw string) {
	v := truncateToLimit([]byte(kw), datasetMaxLen[DS2Keywords])
	if hasHighBytes(v) && !i.isUTF8() {
		i.Records[0] = append(i.Records[0][:0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}
	i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Keywords, Value: v})
}

// SetKeywords replaces all dataset 2:25 (Keywords, IIM §2.2.17) entries in
// record 2 with the provided values. Existing keyword datasets are removed
// first; then one Dataset is appended per keyword. Passing an empty slice
// removes all keywords without adding new ones. Values exceeding 64 bytes are
// truncated at a UTF-8 rune boundary.
func (i *IPTC) SetKeywords(kws []string) {
	if i == nil {
		return
	}
	// Remove all existing DS2Keywords entries from record 2.
	filtered := i.Records[2][:0]
	for _, d := range i.Records[2] {
		if d.DataSet != DS2Keywords {
			filtered = append(filtered, d)
		}
	}
	i.Records[2] = filtered
	// Append one Dataset per keyword (IIM §2.2.17: repeatable).
	for _, kw := range kws {
		v := truncateToLimit([]byte(kw), datasetMaxLen[DS2Keywords])
		i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Keywords, Value: v})
	}
}

// setRecord2 replaces the first occurrence of ds in record 2 with value,
// or appends a new dataset if none exists.
//
// If value contains bytes outside the ASCII range (>0x7F) and the UTF-8 flag
// is not yet set, it is set now. All setters pass Go strings (which are always
// UTF-8) through []byte(s), so any non-ASCII byte in value is a UTF-8 sequence
// that must be declared as such for accessors to read it back correctly.
func (i *IPTC) setRecord2(ds uint8, value []byte) {
	// Auto-upgrade to UTF-8 mode when writing non-ASCII content. This ensures
	// that accessors such as Caption() and Copyright() use the UTF-8 decode path
	// (return string(value)) rather than the ISO-8859-1 decode path, which would
	// produce garbage for multi-byte UTF-8 sequences.
	if hasHighBytes(value) && !i.isUTF8() {
		i.Records[0] = append(i.Records[0][:0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == ds {
			i.Records[2][idx].Value = value
			// Invalidate the decode cache so the new value is re-decoded on
			// the next read (the old decoded string no longer matches Value).
			i.Records[2][idx].decoded = false
			i.Records[2][idx].decodedValue = ""
			return
		}
	}
	i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: ds, Value: value})
}

// firstRecord2 returns the first string value of the given Record 2 dataset.
func (i *IPTC) firstRecord2(ds uint8) string {
	if i == nil {
		return ""
	}
	utf8flag := i.isUTF8()
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == ds {
			return i.Records[2][idx].stringValue(utf8flag)
		}
	}
	return ""
}

// isUTF8 reports whether the stream declared UTF-8 encoding via the
// coded character set dataset (IIM §1.5.1).
func (i *IPTC) isUTF8() bool {
	recs := i.Records[0]
	return len(recs) > 0 && len(recs[0].Value) > 0 && recs[0].Value[0] == 1
}
