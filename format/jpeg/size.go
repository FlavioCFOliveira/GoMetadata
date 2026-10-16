package jpeg

import (
	"errors"
	"fmt"
	"io"
)

// ErrNoFrameHeader is returned by ImageSize when the stream ends, or the
// entropy-coded data starts, before a start-of-frame marker.
var ErrNoFrameHeader = errors.New("jpeg: no frame header")

// ImageSize returns the pixel dimensions of the image that the start-of-frame
// header (SOF0-SOF15 except DHT, JPG and DAC) of the JPEG stream r declares.
// It reads the marker segments from the start of r, skipping each by its
// length with a Seek, and stops at the frame header: a typical Exif-JPEG costs
// a handful of small reads. The position of r is undefined afterwards.
//
// ITU-T T.81 | ISO/IEC 10918-1 §B.2.2 (frame header: Lf, P, Y, X, Nf, with Y
// the number of lines and X the number of samples per line) and §B.1.1.3
// (marker codes); Exif 3.1 §4.7.1.5, p. 103 (SOF in an Exif-JPEG). The values
// are those of the frame header, not the "valid image size" of Exif 3.1 §4.8.1,
// pp. 115-118, which excludes padding added to fill the last MCU: a stream does
// not record the padding.
func ImageSize(r io.ReadSeeker) (width, height uint32, err error) {
	var buf [8]byte
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("jpeg: seek: %w", err)
	}
	if _, err = io.ReadFull(r, buf[:2]); err != nil {
		return 0, 0, fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soiErr := readSOI(buf[:2]); soiErr != nil {
		return 0, 0, soiErr
	}
	for {
		m, n, st := nextSegment(r, buf[:2])
		switch st {
		case segEnd:
			return 0, 0, ErrNoFrameHeader
		case segBare:
			continue
		case segLength:
		}
		if isSOF(m) {
			return readFrameSize(r, n, buf[:5])
		}
		if _, err = r.Seek(n-2, io.SeekCurrent); err != nil {
			return 0, 0, fmt.Errorf("jpeg: seek: %w", err)
		}
	}
}

// segState is what nextSegment found.
type segState uint8

const (
	segLength segState = iota // a marker with a length field
	segBare                   // a marker without one (stuffed 0xFF, TEM, RSTn, SOI)
	segEnd                    // the end of the stream, a damaged stream, or EOI/SOS
)

// nextSegment reads the next marker (0xFF, any number of 0xFF fill bytes, then
// the code; ISO/IEC 10918-1 §B.1.1.2) and, for a marker that has one, the
// length field Lf (which counts its own 2 bytes). scratch is a 2-byte buffer.
// segEnd covers the end of the stream, a byte after a segment that is not 0xFF
// (the stream is damaged), a length below 2, and the markers EOI and SOS: the
// frame header did not come.
func nextSegment(r io.Reader, scratch []byte) (m byte, n int64, st segState) {
	if _, err := io.ReadFull(r, scratch[:1]); err != nil || scratch[0] != 0xFF {
		return 0, 0, segEnd
	}
	for scratch[0] == 0xFF {
		if _, err := io.ReadFull(r, scratch[:1]); err != nil {
			return 0, 0, segEnd
		}
	}
	m = scratch[0]
	if st = markerClass(m); st != segLength {
		return m, 0, st
	}
	if _, err := io.ReadFull(r, scratch[:2]); err != nil {
		return 0, 0, segEnd
	}
	n = int64(scratch[0])<<8 | int64(scratch[1])
	if n < 2 {
		return 0, 0, segEnd
	}
	return m, n, segLength
}

// markerClass tells how a marker code continues: bare markers have no length
// field, EOI and SOS end the search, all others have a length.
func markerClass(m byte) segState {
	switch {
	case m == 0x00 || m == 0x01 || (m >= 0xD0 && m <= 0xD8):
		return segBare
	case m == 0xD9 || m == 0xDA:
		return segEnd
	}
	return segLength
}

// readFrameSize reads P, Y and X of a frame header of length n (Lf) into the
// 5-byte buffer buf.
func readFrameSize(r io.Reader, n int64, buf []byte) (width, height uint32, err error) {
	if n < 8 {
		return 0, 0, ErrNoFrameHeader
	}
	if _, err = io.ReadFull(r, buf); err != nil {
		return 0, 0, ErrNoFrameHeader
	}
	height = uint32(buf[1])<<8 | uint32(buf[2])
	width = uint32(buf[3])<<8 | uint32(buf[4])
	if width == 0 || height == 0 {
		return 0, 0, ErrNoFrameHeader // Y = 0 is DNL-defined: unknown here
	}
	return width, height, nil
}

// isSOF reports whether marker code m starts a frame header: SOF0-SOF15, but
// not 0xC4 (DHT), 0xC8 (JPG) and 0xCC (DAC). ISO/IEC 10918-1 Table B.1.
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}
