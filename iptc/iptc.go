// Package iptc implements an IPTC IIM parser and writer.
//
// Compliance: IPTC IIM version 4.2 (IPTC-NAA Information Interchange Model).
// Spec citations reference the IIM document as "IIM §<section>".
//
// This package operates on the raw IIM byte stream. Extraction of that stream
// from container-specific envelopes (e.g., Photoshop IRB inside JPEG APP13)
// is the responsibility of the container layer (format/jpeg).
package iptc

import "bytes"

// IPTC holds the parsed IPTC datasets grouped by record number.
type IPTC struct {
	// Records maps record number to a slice of datasets in that record.
	// IIM defines records 1–9; record 2 is the most common (envelope and
	// application records per IIM §2).
	Records map[uint8][]Dataset
}

// Dataset is a single IPTC record:dataset value (IIM §1.6).
type Dataset struct {
	Record  uint8
	DataSet uint8
	Value   []byte
}

// Parse parses a raw IPTC IIM byte stream.
// b must begin with (or contain) the IPTC tag marker 0x1C (IIM §1.6).
func Parse(b []byte) (*IPTC, error) {
	i := &IPTC{Records: make(map[uint8][]Dataset)}
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
			// Extended length: (sizeHigh & 0x7F) is the byte count for length.
			nBytes := int(sizeHigh & 0x7F)
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

// Encode serialises i back to an IPTC IIM byte stream.
func Encode(i *IPTC) ([]byte, error) {
	var buf bytes.Buffer
	// Write records in order for deterministic output.
	for record := uint8(1); record <= 9; record++ {
		for _, ds := range i.Records[record] {
			buf.WriteByte(0x1C)
			buf.WriteByte(ds.Record)
			buf.WriteByte(ds.DataSet)
			n := len(ds.Value)
			buf.WriteByte(byte(n >> 8))
			buf.WriteByte(byte(n))
			buf.Write(ds.Value)
		}
	}
	return buf.Bytes(), nil
}

// Copyright returns the value of dataset 2:116 (Copyright Notice, IIM §2.2.28).
func (i *IPTC) Copyright() string {
	return i.firstRecord2(DS2CopyrightNotice)
}

// Caption returns the value of dataset 2:120 (Caption/Abstract, IIM §2.2.29).
func (i *IPTC) Caption() string {
	return i.firstRecord2(DS2Caption)
}

// firstRecord2 returns the first string value of the given Record 2 dataset.
func (i *IPTC) firstRecord2(ds uint8) string {
	if i == nil {
		return ""
	}
	utf8 := i.isUTF8()
	for _, d := range i.Records[2] {
		if d.DataSet == ds {
			return decodeString(d.Value, utf8)
		}
	}
	return ""
}

// isUTF8 reports whether the stream declared UTF-8 encoding via the
// coded character set dataset (IIM §1.5.1).
func (i *IPTC) isUTF8() bool {
	recs, ok := i.Records[0]
	return ok && len(recs) > 0 && len(recs[0].Value) > 0 && recs[0].Value[0] == 1
}
