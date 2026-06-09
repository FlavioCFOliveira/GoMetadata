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
	"slices"
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
	// decodedValue holds the Value decoded to UTF-8. It is populated eagerly
	// by Parse (after the full first pass when the 1:90 charset flag is known)
	// and by every write-path helper that constructs a Dataset. Once set, it
	// is never written by any read accessor, making concurrent reads race-free
	// without synchronisation. Task #60: replaces the former lazy-decode cache
	// (d.decoded bool + d.decodedValue written inside stringValue()) which was
	// a data race when two goroutines called read accessors concurrently.
	decodedValue string
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
// count tracks the total number of Dataset structs stored so far across all
// records; storeDataset increments it and returns false (stop) when the
// maxIPTCDatasets cap is reached, true (continue) otherwise.
func storeDataset(i *IPTC, record, dataset uint8, value []byte, utf8 *bool, count *int) bool {
	// IIM §1.6: valid record numbers are 1–9. Record 0 is an internal
	// pseudo-record (UTF-8 flag) never present on the wire. Any record byte
	// outside [1, len(i.Records)-1] is out of range for the fixed-size array
	// and must be skipped; the caller continues scanning so later valid
	// datasets in the same stream are not lost.
	if record < 1 || int(record) >= len(i.Records) {
		return true
	}
	// Record 1, dataset 90 (1:90) carries the coded character set declaration
	// (IIM §1.5.1). ESC % G signals UTF-8. This is not a stored Dataset so it
	// does not count against maxIPTCDatasets.
	if record == 1 && dataset == 90 {
		*utf8 = isUTF8Declaration(value)
		return true
	}
	// Record-version datasets (1:00, 2:00) are structural markers that IIM §1.6.1
	// and §2.2.1 mandate as the first dataset in their respective records. They
	// carry a uint16 version number (= 4 for IIM 4.x) and carry no application
	// data. We do not store them in Records[] so that callers indexing Records[2]
	// by position see only application datasets (Caption, Copyright, Keywords…).
	// Encode re-emits them automatically (IIM-REC-01/IIM-REC-02, task #153).
	if dataset == 0 && (record == 1 || record == 2) {
		return true
	}
	// Cap total Dataset struct allocations to bound per-struct memory overhead.
	// Each zero-length dataset (5 bytes on wire) allocates ~67 bytes in memory:
	// 13× amplification that the byte-aggregate cap (maxIPTCTotalBytes) misses
	// because it only counts value bytes (task #71).
	*count++
	if *count > maxIPTCDatasets {
		return false
	}
	// We store datasets as raw bytes; charset decoding happens on access
	// (see firstRecord2 / stringValue).
	i.Records[record] = append(i.Records[record], Dataset{
		Record:  record,
		DataSet: dataset,
		Value:   value,
	})
	return true
}

// maxIPTCTotalBytes is the maximum aggregate size of all parsed IPTC dataset
// values. This prevents memory exhaustion from streams with many large datasets.
const maxIPTCTotalBytes = 256 << 20 // 256 MiB

// maxIPTCDatasets is the maximum total number of Dataset structs that Parse
// will store across all records. Each Dataset is ~67 bytes on 64-bit; 65 536
// entries cap the struct-allocation budget at ~4 MiB regardless of value size.
// This bounds the amplification attack where N zero-length datasets (5 bytes
// each on wire) bypass the byte-aggregate cap (maxIPTCTotalBytes counts value
// bytes only, not struct overhead). When the cap is reached Truncated is set
// and parsing stops.
const maxIPTCDatasets = 65536

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
func Parse(b []byte) (*IPTC, error) { //nolint:gocyclo // IIM scanner has inherent branching (tag-marker scan, standard/extended length, per-dataset guards, aggregate cap); the post-parse decode pass adds one loop but extracting it reduces cohesion without reducing real complexity
	i := new(IPTC)
	// Pre-allocate record 2 (Application Record) — the most common record,
	// typically containing 5–15 datasets in a production JPEG (IIM §2).
	i.Records[2] = make([]Dataset, 0, 12)
	utf8 := false
	totalBytes := 0
	datasetCount := 0 // tracks total Dataset structs stored; capped at maxIPTCDatasets (task #71)

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
		// The aggregate DoS guards (totalBytes > maxIPTCTotalBytes and
		// datasetCount > maxIPTCDatasets) are the only irrecoverable conditions:
		// we stop there to bound total memory use.
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

		if !storeDataset(i, record, dataset, value, &utf8, &datasetCount) {
			// maxIPTCDatasets cap reached: struct-allocation bomb guard (task #71).
			// A stream of N zero-length datasets contributes 0 to totalBytes but
			// allocates one Dataset struct each (~67 bytes). Cap total structs at
			// maxIPTCDatasets (~4 MiB) regardless of value size.
			i.Truncated = true
			break
		}
	}

	// Store the UTF-8 flag as a pseudo-dataset in record 0 so convenience
	// methods can retrieve it without re-scanning record 1.
	if utf8 {
		i.Records[0] = append(i.Records[0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}

	// Task #60: eager pre-decode pass. Now that the full stream has been scanned
	// and the UTF-8 flag (from 1:90) is final, decode every dataset value to
	// UTF-8 and store the result in decodedValue. Read accessors return this
	// pre-decoded string directly, so they never write to the Dataset after
	// Parse returns — concurrent reads are race-free without synchronisation.
	//
	// The 1:90 declaration can appear at any position in the stream (IIM places
	// no ordering constraint), which is why decoding must happen after the loop
	// rather than inline during dataset storage.
	for rec := range i.Records {
		for idx := range i.Records[rec] {
			i.Records[rec][idx].setDecodedValue(utf8)
		}
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
func Encode(i *IPTC) ([]byte, error) { //nolint:gocyclo,cyclop // complexity is inherent: UTF-8 auto-declaration, record-version injection, ascending-sort, extended-length encoding all require distinct branches
	buf := encBufPool.Get().(*bytes.Buffer) //nolint:forcetypeassert,revive // encBufPool.New always stores *bytes.Buffer; pool invariant
	buf.Reset()

	// Determine whether to emit the coded character set declaration (IIM §1.5.1).
	// Emit it when:
	//   (a) the stream was parsed with an existing 1:90 UTF-8 declaration, OR
	//   (b) the stream has no 1:90 declaration but contains non-ASCII bytes.
	// Case (b) is the auto-inject path for #19: writing "café" into a fresh
	// *IPTC (no declaration) must produce a stream that round-trips correctly.
	emitUTF8Decl := i.isUTF8() || i.needsUTF8Declaration()

	// #179 fix: track whether 1:00 EnvelopeRecordVersion was already emitted by
	// the UTF-8 preamble path so the main-loop Record-1 injection can be skipped.
	// IIM §1.5(v) MUST-NOT-REPEAT: exactly one 1:00 may appear per stream.
	emittedR1Version := false

	if emitUTF8Decl {
		// IIM-REC-01 / IIM §1.6.1: when Record 1 is present, 1:00
		// EnvelopeRecordVersion MUST be the first dataset, value = uint16 BE 4.
		// We emit 1:90 (CodedCharacterSet) which is the only Record-1 dataset
		// this library writes. 1:00 must precede it in the stream.
		// Emit it unconditionally here to satisfy IIM-REC-01 whenever R1 is written.
		buf.Write([]byte{0x1C, 0x01, 0x00, 0x00, 0x02, 0x00, 0x04})
		emittedR1Version = true // #179: record emission to prevent duplication below

		// Record 1, Dataset 90: coded character set = UTF-8 (ESC % G).
		// IIM §1.5.1: ESC % G (0x1B 0x25 0x47) is the ISO 2022 designation for UTF-8.
		//
		// FINDING-002 fix: Encode must NOT mutate its receiver. The previous
		// implementation appended to i.Records[0] here to update the internal UTF-8
		// flag, which caused a data race when two goroutines called Encode (or Write)
		// concurrently on the same *IPTC carrying non-ASCII content — they both wrote
		// to the Records[0] slice header without synchronisation.
		//
		// The fix: emit the 1:90 declaration into the encoded byte stream only.
		// The receiver is left unchanged. Callers that need the UTF-8 flag set on the
		// *IPTC after encoding should call Parse on the encoded output (which is the
		// canonical path — Encode produces a byte stream, not a new *IPTC).
		buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47})
	}

	// Write records in order for deterministic output.
	for record := uint8(1); record <= 9; record++ {
		datasets := i.Records[record]
		if len(datasets) == 0 {
			continue
		}

		// #146 fix: IIM §2.2 SHOULD — datasets within a record should be emitted
		// in ascending DataSet-number order. Sort a local copy so that the receiver
		// is never mutated (FINDING-002 constraint: Encode must be side-effect-free).
		// slices.SortStableFunc preserves the relative order of equal DataSet numbers
		// (important for repeatable datasets like 2:25 Keywords, 2:80 By-line).
		// IPTC-NAA IIM 4.2 §2.2: the Application Record datasets shall be ordered
		// by dataset number within the record (SHOULD).
		sorted := slices.Clone(datasets) // clone is intentional: must not mutate receiver (FINDING-002)
		slices.SortStableFunc(sorted, func(a, b Dataset) int {
			return int(a.DataSet) - int(b.DataSet)
		})

		// IIM-REC-02 / IIM §2.2.1: when Record 2 is present, 2:00
		// ApplicationRecordVersion MUST be the first dataset, value = uint16 BE 4.
		// Emit it automatically unless the caller already stored a 2:00 entry.
		// Same logic applies to Record 1 (IIM-REC-01 / IIM §1.6.1): 1:00 must be
		// present when Record 1 is emitted. For Record 1: skip if already emitted
		// by the UTF-8 preamble above (#179 fix: prevents duplicate 1:00).
		if record == 1 || record == 2 {
			// Check whether a version dataset (ds number 0) is already present.
			// After the ascending sort, a version dataset will be first if present.
			hasVersion := len(sorted) > 0 && sorted[0].DataSet == 0
			// #179: for Record 1, skip the 1:00 injection when the UTF-8
			// preamble already emitted it. IIM §1.5(v) MUST-NOT-REPEAT.
			// De Morgan's law applied: !hasVersion && (record != 1 || !emittedR1Version).
			if !hasVersion && (record != 1 || !emittedR1Version) {
				// Emit the mandatory record-version dataset first.
				// IIM §2.2.1 (Record 2) / §1.6.1 (Record 1): value = big-endian uint16 = 4.
				buf.Write([]byte{0x1C, record, 0x00, 0x00, 0x02, 0x00, 0x04})
			}
		}

		for _, ds := range sorted {
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
	// Pre-count to allocate the result slice in one shot, avoiding repeated
	// append growth. Each string copy is a header copy (no allocation).
	n := 0
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Keywords {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	result := make([]string, 0, n)
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Keywords {
			result = append(result, i.Records[2][idx].decodedValue)
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
	// Pre-count to allocate the result slice in one shot.
	n := 0
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Byline {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	result := make([]string, 0, n)
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Byline {
			result = append(result, i.Records[2][idx].decodedValue)
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
	i.setUTF8IfNeeded(v)
	d := Dataset{Record: 2, DataSet: DS2Byline, Value: v}
	d.setDecodedValue(true) // write-path: v is always UTF-8 (truncated from a Go string)
	i.Records[2] = append(i.Records[2], d)
}

// AddKeyword appends a keyword to dataset 2:25 (Keywords, IIM §2.2.17).
// Keywords is a repeatable dataset; each call adds one additional entry.
// Values exceeding 64 bytes are truncated at a UTF-8 rune boundary.
func (i *IPTC) AddKeyword(kw string) {
	v := truncateToLimit([]byte(kw), datasetMaxLen[DS2Keywords])
	i.setUTF8IfNeeded(v)
	d := Dataset{Record: 2, DataSet: DS2Keywords, Value: v}
	d.setDecodedValue(true) // write-path: v is always UTF-8 (truncated from a Go string)
	i.Records[2] = append(i.Records[2], d)
}

// setUTF8IfNeeded sets the internal UTF-8 flag (Records[0]) when v contains
// bytes outside the ASCII range and the flag is not yet set. All write-path
// helpers that accept user-supplied strings call this so that in-memory reads
// via accessors (Keywords, Caption, etc.) return correct UTF-8 strings without
// an Encode/Parse round-trip. Without this guard the accessors would fall back
// to the ISO-8859-1 decode path and produce mojibake for multi-byte sequences.
//
// Thread-safety: not safe for concurrent use. Write-path helpers are not
// designed to be called concurrently on the same *IPTC; only read accessors
// need to be concurrent-safe (task #60 addresses that separately).
func (i *IPTC) setUTF8IfNeeded(v []byte) {
	if hasHighBytes(v) && !i.isUTF8() {
		i.Records[0] = append(i.Records[0][:0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}
}

// SetKeywords replaces all dataset 2:25 (Keywords, IIM §2.2.17) entries in
// record 2 with the provided values. Existing keyword datasets are removed
// first; then one Dataset is appended per keyword. Passing an empty slice
// removes all keywords without adding new ones. Values exceeding 64 bytes are
// truncated at a UTF-8 rune boundary.
//
// If any keyword contains bytes outside the ASCII range the UTF-8 flag is set
// so that Keywords() returns correct UTF-8 strings before an Encode/Parse
// round-trip (task #63 fix: mirrors the guard already present in AddKeyword
// and setRecord2).
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
	// Set the UTF-8 flag before appending so that the first keyword read-back
	// already sees the correct charset context.
	for _, kw := range kws {
		v := truncateToLimit([]byte(kw), datasetMaxLen[DS2Keywords])
		i.setUTF8IfNeeded(v) // task #63: set UTF-8 flag when keyword has non-ASCII bytes
		d := Dataset{Record: 2, DataSet: DS2Keywords, Value: v}
		d.setDecodedValue(true) // write-path: v is always UTF-8 (truncated from a Go string)
		i.Records[2] = append(i.Records[2], d)
	}
}

// setRecord2 replaces the first occurrence of ds in record 2 with value,
// or appends a new dataset if none exists.
//
// If value contains bytes outside the ASCII range (>0x7F) and the UTF-8 flag
// is not yet set, it is set now. All setters pass Go strings (which are always
// UTF-8) through []byte(s), so any non-ASCII byte in value is a UTF-8 sequence
// that must be declared as such for accessors to read it back correctly.
//
// After updating or appending the Dataset, decodedValue is set immediately to
// string(value) (the caller always supplies UTF-8 bytes, so no ISO-8859-1
// decode is needed on the write path).
func (i *IPTC) setRecord2(ds uint8, value []byte) {
	// Auto-upgrade to UTF-8 mode when writing non-ASCII content. This ensures
	// that accessors such as Caption() and Copyright() use the UTF-8 decode path
	// (return string(value)) rather than the ISO-8859-1 decode path, which would
	// produce garbage for multi-byte UTF-8 sequences.
	i.setUTF8IfNeeded(value)
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == ds {
			i.Records[2][idx].Value = value
			// Re-decode immediately: write-path values are always UTF-8 (Go strings),
			// so isUTF8=true. decodedValue = string(value) via setDecodedValue.
			i.Records[2][idx].setDecodedValue(true)
			return
		}
	}
	d := Dataset{Record: 2, DataSet: ds, Value: value}
	d.setDecodedValue(true) // write-path: value is always UTF-8
	i.Records[2] = append(i.Records[2], d)
}

// firstRecord2 returns the first string value of the given Record 2 dataset.
func (i *IPTC) firstRecord2(ds uint8) string {
	if i == nil {
		return ""
	}
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == ds {
			return i.Records[2][idx].decodedValue
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
