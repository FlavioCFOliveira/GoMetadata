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
)

// IPTC holds the parsed IPTC datasets grouped by record number.
type IPTC struct {
	// Records holds datasets indexed by record number (0–9).
	// IIM defines records 1–9; index 0 is a pseudo-record used internally
	// to store the UTF-8 flag (see isUTF8). Using a fixed-size array instead
	// of a map eliminates the map allocation entirely — one fewer heap object
	// per Parse call — and allows O(1) index access without hashing.
	Records [10][]Dataset
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

// Parse parses a raw IPTC IIM byte stream.
// b must begin with (or contain) the IPTC tag marker 0x1C (IIM §1.6).
func Parse(b []byte) (*IPTC, error) {
	i := new(IPTC)
	// Pre-allocate record 2 (Application Record) — the most common record,
	// typically containing 5–15 datasets in a production JPEG (IIM §2).
	i.Records[2] = make([]Dataset, 0, 12)
	utf8 := false

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

		record := b[pos+1]
		dataset := b[pos+2]

		// Data length field (IIM §1.6.2).
		// If bit 15 of the 2-byte size field is set, the following n bytes
		// (n = lower 15 bits) carry the actual length.
		sizeHigh := b[pos+3]
		sizeLow := b[pos+4]
		pos += 5

		var length int
		if sizeHigh&0x80 != 0 {
			// Extended length encoding (IIM §1.6.2): bit 15 of the 2-byte size
			// field is set. The remaining 15 bits carry the byte count for the
			// actual length value: nBytes = (sizeHigh & 0x7F) << 8 | sizeLow.
			nBytes := int(sizeHigh&0x7F)<<8 | int(sizeLow)
			if nBytes < 1 || nBytes > 4 || pos+nBytes > len(b) {
				break
			}
			for j := 0; j < nBytes; j++ {
				length = length<<8 | int(b[pos+j])
			}
			pos += nBytes
		} else {
			length = int(sizeHigh)<<8 | int(sizeLow)
		}

		// Cap individual dataset size to 1 MiB to prevent memory exhaustion
		// from crafted IPTC streams with large declared lengths.
		if length > 1<<20 {
			break
		}
		if length < 0 || pos+length > len(b) {
			break
		}

		value := b[pos : pos+length]
		pos += length

		// Record 1, dataset 90 (1:90) carries the coded character set declaration
		// (IIM §1.5.1). ESC % G signals UTF-8.
		if record == 1 && dataset == 90 {
			utf8 = isUTF8Declaration(value)
			continue
		}

		// Record 1, dataset 90 constant is also named DS2_CodedCharacterSet but
		// shares numeric value 90 with DS2_City — dataset.go comment explains this.
		// We store datasets as raw bytes; decoding on access (see firstRecord2).
		i.Records[record] = append(i.Records[record], Dataset{
			Record:  record,
			DataSet: dataset,
			Value:   value,
		})
	}

	// Store the UTF-8 flag as a pseudo-dataset in record 0 so convenience
	// methods can retrieve it without re-scanning record 1.
	if utf8 {
		i.Records[0] = append(i.Records[0], Dataset{Record: 0, DataSet: 0, Value: []byte{1}})
	}

	return i, nil
}

// encBufPool reuses bytes.Buffer allocations across Encode calls. This avoids
// repeated heap allocation of the buffer's internal byte array on every call.
// The result is always a fresh bytes.Clone of the buffer contents, so the
// returned slice is safe to use after the buffer is returned to the pool.
var encBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }} //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// Encode serialises i back to an IPTC IIM byte stream.
func Encode(i *IPTC) ([]byte, error) {
	buf := encBufPool.Get().(*bytes.Buffer)
	buf.Reset()

	// Re-emit the coded character set declaration (IIM §1.5.1) when the
	// original stream declared UTF-8 (ESC % G = 0x1B 0x25 0x47).
	if i.isUTF8() {
		// Record 1, Dataset 90: coded character set = UTF-8.
		buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47})
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
				buf.WriteByte(0x80) // bit 15 set; upper 7 bits of count = 0
				buf.WriteByte(0x04) // lower 8 bits of count = 4
				buf.WriteByte(byte(n >> 24))
				buf.WriteByte(byte(n >> 16))
				buf.WriteByte(byte(n >> 8))
				buf.WriteByte(byte(n))
			} else {
				buf.WriteByte(byte(n >> 8))
				buf.WriteByte(byte(n))
			}
			buf.Write(ds.Value)
		}
	}
	result := bytes.Clone(buf.Bytes())
	encBufPool.Put(buf)
	return result, nil
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
	utf8 := i.isUTF8()
	var result []string
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == DS2Keywords {
			result = append(result, i.Records[2][idx].stringValue(utf8))
		}
	}
	return result
}

// Creator returns the value of dataset 2:80 (By-line / author, IIM §2.2.25).
func (i *IPTC) Creator() string {
	return i.firstRecord2(DS2Byline)
}

// SetCaption sets dataset 2:120 (Caption/Abstract) to s, replacing any existing value.
func (i *IPTC) SetCaption(s string) {
	i.setRecord2(DS2Caption, []byte(s))
}

// SetCopyright sets dataset 2:116 (Copyright Notice) to s, replacing any existing value.
func (i *IPTC) SetCopyright(s string) {
	i.setRecord2(DS2CopyrightNotice, []byte(s))
}

// SetCreator sets dataset 2:80 (By-line) to s, replacing any existing value.
func (i *IPTC) SetCreator(s string) {
	i.setRecord2(DS2Byline, []byte(s))
}

// AddKeyword appends a keyword to dataset 2:25 (Keywords, IIM §2.2.17).
// Keywords is a repeatable dataset; each call adds one additional entry.
func (i *IPTC) AddKeyword(kw string) {
	i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Keywords, Value: []byte(kw)})
}

// SetKeywords replaces all dataset 2:25 (Keywords, IIM §2.2.17) entries in
// record 2 with the provided values. Existing keyword datasets are removed
// first; then one Dataset is appended per keyword. Passing an empty slice
// removes all keywords without adding new ones.
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
		i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Keywords, Value: []byte(kw)})
	}
}

// setRecord2 replaces the first occurrence of ds in record 2 with value,
// or appends a new dataset if none exists.
func (i *IPTC) setRecord2(ds uint8, value []byte) {
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
	utf8 := i.isUTF8()
	for idx := range i.Records[2] {
		if i.Records[2][idx].DataSet == ds {
			return i.Records[2][idx].stringValue(utf8)
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
