package jpeg

// Adapted from metadata-extractor Source/com/drew/metadata/jpeg/JpegDnlReader.java:JpegDnlReader.extract, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0
// Changes: only the rule is adapted (the number of lines of the frame is the first unsigned 16-bit value of the DNL segment, used only when the frame header declares none); metadata-extractor stops at the first SOS and never reaches the DNL segment, so the search for it in the entropy-coded data of the first scan (scanForDNL, nextMarker), its limit and the checks of Ld and NL (ITU-T T.81 Table B.10) are original (task #365).

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// maxDNLScan bounds the entropy-coded bytes ImageSize reads to find a DNL
// marker (task #365). The DNL segment follows the first scan, so the search
// reads that scan once, at the speed of a byte search: 64 MiB is far beyond
// any first scan of a frame whose height is deferred (scanner and fax
// output), and a larger one is reported as having no height instead of being
// read to its end.
const maxDNLScan = 64 << 20

// markerDNL is the define-number-of-lines marker code, ITU-T T.81 Table B.1
// (X'FFDC').
const markerDNL = 0xDC

// ErrNoFrameHeader is returned by ImageSize when the stream ends, or the
// entropy-coded data starts, before a start-of-frame marker.
var ErrNoFrameHeader = errors.New("jpeg: no frame header")

// ImageSize returns the pixel dimensions of the image that the start-of-frame
// header (SOF0-SOF15 except DHT, JPG and DAC) of the JPEG stream r declares.
// It reads the marker segments from the start of r, skipping each by its
// length with a Seek, and stops at the frame header: a typical Exif-JPEG costs
// a handful of small reads. The position of r is undefined afterwards.
//
// ITU-T T.81 | ISO/IEC 10918-1 §B.2.2, p. 36 (frame header: Lf, P, Y, X, Nf,
// with Y the number of lines and X the number of samples per line) and §B.1.1.3
// (marker codes); Exif 3.1 §4.7.1.5, p. 103 (SOF in an Exif-JPEG). A frame
// header with Y = 0 defers the number of lines to the define-number-of-lines
// (DNL) segment that follows the first scan (§B.2.2, p. 36: "Value 0 indicates
// that the number of lines shall be defined by the DNL marker and parameters at
// the end of the first scan"; §B.2.5, p. 45: the segment is mandatory when Y is
// 0 and carries NL, 1-65535; §B.2.1, p. 35: it immediately follows the first
// scan). ImageSize then reads the remaining frame-level segments up to the
// first start-of-scan, skips the first scan by searching its entropy-coded data
// for the next marker other than a stuffed zero or a restart marker (§B.1.1.5),
// and takes NL from the DNL segment found there; the search reads at most
// maxDNLScan bytes and never decodes. The values
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
			return sofSize(r, n, buf[:5])
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

// sofSize reads the frame header of length n (Lf) that r is positioned in, and
// returns its size; a header with Y = 0 takes the number of lines from the DNL
// segment (deferredHeight). buf has room for the 5 bytes of P, Y and X.
func sofSize(r io.ReadSeeker, n int64, buf []byte) (width, height uint32, err error) {
	width, height, err = readFrameSize(r, n, buf)
	if err != nil || height != 0 {
		return width, height, err
	}
	return deferredHeight(r, width, n-7, buf[:2])
}

// readFrameSize reads P, Y and X of a frame header of length n (Lf) into the
// 5-byte buffer buf. The height is 0 when the frame header defers it to a DNL
// segment; the width is never 0 without an error.
func readFrameSize(r io.Reader, n int64, buf []byte) (width, height uint32, err error) {
	if n < 8 {
		return 0, 0, ErrNoFrameHeader
	}
	if _, err = io.ReadFull(r, buf); err != nil {
		return 0, 0, ErrNoFrameHeader
	}
	height = uint32(buf[1])<<8 | uint32(buf[2])
	width = uint32(buf[3])<<8 | uint32(buf[4])
	if width == 0 {
		return 0, 0, ErrNoFrameHeader
	}
	return width, height, nil // height 0: defined by a DNL segment (ImageSize)
}

// isSOF reports whether marker code m starts a frame header: SOF0-SOF15, but
// not 0xC4 (DHT), 0xC8 (JPG) and 0xCC (DAC). ISO/IEC 10918-1 Table B.1.
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

// deferredHeight finds the number of lines of a frame whose header declared
// Y = 0, in the DNL segment that follows the first scan (ITU-T T.81 §B.2.5,
// p. 45). r is positioned inside the frame header, rest bytes before its end.
// It skips the rest of the frame header and every segment up to the first
// start-of-scan, skips the scan header, and searches the entropy-coded data of
// the first scan for the DNL segment (scanForDNL). ErrNoFrameHeader means the
// stream has no DNL segment where the standard puts it.
//
// Reference: metadata-extractor Source/com/drew/metadata/jpeg/JpegDnlReader.java
// (Apache-2.0, 6f037943) takes the height from the DNL segment only when the
// frame header's is 0 and reads NL as the first unsigned 16-bit value of the
// segment, as done here; libjpeg-turbo src/jdmarker.c get_sof (IJG/BSD,
// 2fa570c5) rejects Y = 0 ("DNL not supported"), so it offers no search to
// adapt; the search for the marker is written from §B.1.1.5.
func deferredHeight(r io.ReadSeeker, width uint32, rest int64, scratch []byte) (uint32, uint32, error) {
	if _, err := r.Seek(rest, io.SeekCurrent); err != nil {
		return 0, 0, fmt.Errorf("jpeg: seek: %w", err)
	}
	for {
		m, n, st := nextSegment(r, scratch)
		switch {
		case st == segBare:
			continue
		case st == segEnd && m != markerSOS: // EOI, the end of the stream or damage: no scan
			return 0, 0, ErrNoFrameHeader
		case st == segEnd:
			height, err := firstScanHeight(r, scratch)
			return width, height, err
		}
		if _, err := r.Seek(n-2, io.SeekCurrent); err != nil {
			return 0, 0, fmt.Errorf("jpeg: seek: %w", err)
		}
	}
}

// firstScanHeight reads the scan header that r is positioned at (after the SOS
// marker, whose length field comes first) and returns the number of lines of
// the DNL segment that ends the scan.
func firstScanHeight(r io.ReadSeeker, scratch []byte) (uint32, error) {
	if _, err := io.ReadFull(r, scratch[:2]); err != nil {
		return 0, ErrNoFrameHeader
	}
	ls := int64(scratch[0])<<8 | int64(scratch[1])
	if ls < 2 {
		return 0, ErrNoFrameHeader
	}
	if _, err := r.Seek(ls-2, io.SeekCurrent); err != nil {
		return 0, fmt.Errorf("jpeg: seek: %w", err)
	}
	return scanForDNL(r, maxDNLScan)
}

// scanForDNL reads the entropy-coded data of a scan from r and returns NL of
// the DNL segment that ends it. Inside the data a 0xFF byte is followed by 0x00
// (a stuffed byte) or by a restart marker RSTm (0xD0-0xD7), or is a fill byte
// before a marker; any other marker ends the scan (ITU-T T.81 §B.1.1.5). The
// scan ends in a DNL segment (§B.2.5, p. 45: Ld, then NL; Ld is at least 4) or
// the frame has no number of lines: ErrNoFrameHeader. At most maxDNLScan bytes
// are read (limit; maxDNLScan in ImageSize).
func scanForDNL(r io.Reader, limit int) (uint32, error) {
	bufPtr := iobuf.Get(iobuf.StreamCopyBufSize)
	defer iobuf.Put(bufPtr)
	buf := *bufPtr
	afterFF := false // the previous byte was a 0xFF that starts a marker
	for read := 0; read < limit; {
		n, err := r.Read(buf)
		read += n
		code, rest, pending, found := nextMarker(buf[:n], afterFF)
		afterFF = pending
		switch {
		case found && code == markerDNL:
			return readDNL(r, rest)
		case found: // EOI, another scan, a table: no DNL segment
			return 0, ErrNoFrameHeader
		case err != nil:
			return 0, ErrNoFrameHeader
		}
	}
	return 0, ErrNoFrameHeader
}

// nextMarker returns the first marker code of chunk that is not part of the
// entropy-coded data: inside the data a 0xFF byte is followed by 0x00 (a stuffed
// byte) or by a restart marker RSTm (0xD0-0xD7), or is a fill byte before a
// marker (ITU-T T.81 §B.1.1.5). afterFF says that the byte before chunk was a
// 0xFF; pending is that state at the end of chunk when no marker was found.
func nextMarker(chunk []byte, afterFF bool) (code byte, rest []byte, pending, found bool) {
	for len(chunk) > 0 {
		if !afterFF {
			i := bytes.IndexByte(chunk, 0xFF)
			if i < 0 {
				return 0, nil, false, false
			}
			chunk, afterFF = chunk[i+1:], true
			continue
		}
		b := chunk[0]
		chunk = chunk[1:]
		switch {
		case b == 0xFF: // fill byte: the marker code follows
		case b == 0x00 || (b >= 0xD0 && b <= 0xD7): // stuffed byte or RSTm
			afterFF = false
		default:
			return b, chunk, false, true
		}
	}
	return 0, nil, afterFF, false
}

// readDNL reads Ld and NL of a DNL segment whose first bytes are in head (the
// bytes of the read buffer after the marker) and the rest in r.
func readDNL(r io.Reader, head []byte) (uint32, error) {
	var seg [4]byte
	k := copy(seg[:], head)
	if k < len(seg) {
		if _, err := io.ReadFull(r, seg[k:]); err != nil {
			return 0, ErrNoFrameHeader
		}
	}
	ld := uint32(seg[0])<<8 | uint32(seg[1])
	nl := uint32(seg[2])<<8 | uint32(seg[3])
	if ld < 4 || nl == 0 {
		return 0, ErrNoFrameHeader // Ld 4-65535 and NL 1-65535, Table B.10
	}
	return nl, nil
}
