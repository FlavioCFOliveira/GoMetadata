// Package irb parses and edits Photoshop Image Resource Blocks (IRB), the
// "8BIM" resource stream that carries the IPTC-NAA record (resource 0x0404)
// and its MD5 digest (resource 0x0425). The stream is embedded in JPEG APP13
// segments, in TIFF tag 0x8649 (and in TIFF-based RAW files), and in PNG
// "Raw profile type iptc" text chunks, so one parser serves every container.
//
// The resource walk and the digest handling are adapted from the Adobe XMP
// Toolkit SDK (see the attribution lines below the package clause and
// THIRD_PARTY_NOTICES.md). Changes from the Toolkit:
//   - the resource walk is bounds-checked in uint64, so it is safe on 32-bit
//     platforms;
//   - a bad Pascal-name length is a recoverable scan-forward miss instead of
//     the end of the walk;
//   - resources are returned as sub-slices of the input (zero copy) instead
//     of being copied into a map;
//   - the digest is created only when the caller also writes XMP, and an
//     existing digest is refreshed only when it matched the IIM it replaces; a
//     stale digest stays stale (tasks #284, #323), whereas the Toolkit always
//     rewrites it;
//   - the digest check also accepts the zero-padded form of a TIFF tag 0x83BB
//     value (DigestCurrent), as the Toolkit's TIFF handler does;
//   - duplicate resources follow the Toolkit's policy (first copy wins, a later
//     non-empty copy replaces an empty first one) in every reader and in the
//     splice, which keeps one 0x0404 resource (task #313).
//
// Layout of one resource (Adobe Photoshop File Formats Specification,
// "Image Resource Blocks"; EXIF/IPTC conformance notes iptc.md §2):
//
//	"8BIM" | resource ID (2, big endian) | Pascal name (1 length byte + name,
//	padded so the field has even length) | data size (4, big endian) | data
//	| 1 pad byte when the data size is odd
package irb

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/PSIR_MemoryReader.cpp:PSIR_MemoryReader::ParseMemoryResources, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/PSIR_FileWriter.cpp:PSIR_FileWriter::ParseMemoryResources, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileIPTC.cpp:ComputeIPTCDigest,SetIPTCDigest, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FileHandlers/TIFF_Handler.cpp:TIFF_MetaHandler::ProcessXMP, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G401/G501: MD5 is mandated by MWG Guidelines v2.0 §4.2.3.2 for the IPTC digest; not used for security
	"encoding/binary"
)

const (
	// ResourceIPTC is the IPTC-NAA record resource (Adobe Photoshop File
	// Formats Specification, resource 1028).
	ResourceIPTC uint16 = 0x0404
	// ResourceIPTCDigest is the 16-byte MD5 of the IPTC-NAA record resource
	// (resource 1061; MWG Guidelines v2.0 §4.2.3.2).
	ResourceIPTCDigest uint16 = 0x0425
	// DigestSize is the size in bytes of an IPTC digest (an MD5 sum).
	DigestSize = 16
)

// skipPascalString advances pos past the Pascal-string name field of a
// resource: 1 length byte, the name, and one pad byte so the field length is
// even. It returns (newPos, true) on success and (pos+1, false) when the
// buffer is too short or the declared name overruns it.
//
// Derived from PSIR_MemoryReader::ParseMemoryResources:
// psirPtr += ((nameLen + 2) & 0xFFFE).
func skipPascalString(b []byte, pos int) (int, bool) {
	if pos >= len(b) {
		return pos + 1, false
	}
	nameLen := int(b[pos])
	pos++ // length byte
	pos += nameLen
	// A crafted name length can run past the buffer; report a recoverable
	// miss so the caller keeps scanning for later valid 8BIM blocks.
	if pos > len(b) {
		return pos, false
	}
	if (nameLen+1)%2 != 0 {
		pos++ // even-padding byte
	}
	return pos, true
}

// ParseEntry validates the "8BIM" signature at b[pos] and returns the
// resource ID, its data (a sub-slice of b), the position just after the data,
// and ok.
//
// Failure modes the caller must distinguish:
//   - signature mismatch or overflowing Pascal name: newPos == pos, a
//     recoverable miss; the caller advances one byte and scans forward;
//   - truncated header or data that overruns b: newPos > pos, terminal.
//
// The data size is validated in uint64 before any conversion to int so a size
// of 2 GiB or more cannot turn negative on 32-bit platforms (task #45).
func ParseEntry(b []byte, pos int) (resourceID uint16, data []byte, newPos int, ok bool) {
	entryPos := pos
	if pos+4 > len(b) {
		return 0, nil, pos + 1, false
	}
	if b[pos] != '8' || b[pos+1] != 'B' || b[pos+2] != 'I' || b[pos+3] != 'M' {
		return 0, nil, pos, false
	}
	pos += 4
	if pos+2 > len(b) {
		return 0, nil, pos + 1, false
	}
	resourceID = binary.BigEndian.Uint16(b[pos:])
	pos += 2

	pos, ok = skipPascalString(b, pos)
	if !ok {
		return 0, nil, entryPos, false
	}
	if pos+4 > len(b) {
		return 0, nil, pos + 1, false
	}
	size := uint64(binary.BigEndian.Uint32(b[pos:]))
	pos += 4
	if uint64(pos)+size > uint64(len(b)) { //nolint:gosec // G115: pos is non-negative and bounded by len(b)
		return 0, nil, pos + 1, false
	}
	n := int(size) //nolint:gosec // G115: size <= len(b)-pos, so it fits int
	return resourceID, b[pos : pos+n], pos + n, true
}

// next returns the position of the resource that follows data, applying the
// even-padding rule and clamping to len(b).
func next(b []byte, newPos, dataLen int) int {
	if dataLen%2 != 0 {
		newPos++
	}
	return min(newPos, len(b))
}

// resource is one well-formed resource found by a scanner.
type resource struct {
	start     int // offset of the "8BIM" signature
	dataStart int // offset of the data
	end       int // offset of the next resource (data plus pad byte)
	id        uint16
	data      []byte // aliases the scanned stream
}

// scanner walks the resources of an IRB stream, skipping bytes that do not
// start a resource (a recoverable miss scans forward by one byte, as
// PSIR_MemoryReader::ParseMemoryResources tolerates junk between resources)
// and stopping at the first structural failure. It allocates nothing.
type scanner struct {
	b   []byte
	pos int
}

// next returns the next resource, or false at the end of the stream.
func (s *scanner) next() (resource, bool) {
	for s.pos < len(s.b) {
		id, data, newPos, ok := ParseEntry(s.b, s.pos)
		if !ok {
			if newPos == s.pos {
				s.pos++ // signature mismatch: scan forward
				continue
			}
			s.pos = len(s.b) // structural failure: terminal
			break
		}
		r := resource{start: s.pos, dataStart: newPos - len(data), end: next(s.b, newPos, len(data)), id: id, data: data}
		s.pos = r.end
		return r, true
	}
	return resource{}, false
}

// Duplicate resources. An IRB should hold at most one resource of each ID,
// but streams with two 0x0404 resources occur in the field (one of them often
// empty). Every reader and writer of this package resolves them with the
// policy of the Adobe XMP Toolkit
// (PSIR_FileWriter.cpp:PSIR_FileWriter::ParseMemoryResources): the first
// resource of an ID wins, except that a later non-empty resource replaces an
// empty first one. For the digest (0x0425) only resources of exactly
// DigestSize bytes are candidates, so the first such resource wins.

// iptcWinner returns the start offset and data of the 0x0404 resource the
// duplicate policy selects, and false when b holds none. Like the other
// read-path walkers it loops over ParseEntry directly (see FindIPTC).
func iptcWinner(b []byte) (start int, data []byte, ok bool) {
	pos := 0
	for pos < len(b) {
		id, d, newPos, valid := ParseEntry(b, pos)
		if !valid {
			if newPos == pos {
				pos++ // signature mismatch: scan forward
				continue
			}
			break // structural failure
		}
		if id == ResourceIPTC && preferIPTC(ok, data, d) {
			start, data, ok = pos, d, true
			if len(d) > 0 {
				return start, data, true // a non-empty first copy is final
			}
		}
		pos = next(b, newPos, len(d))
	}
	return start, data, ok
}

// preferIPTC reports whether the 0x0404 resource cand replaces the current
// choice cur under the duplicate policy: it is the first one (have false), or
// cur is empty and cand is not.
func preferIPTC(have bool, cur, cand []byte) bool {
	return !have || (len(cur) == 0 && len(cand) > 0)
}

// FindIPTC returns the data of the 0x0404 resource of b selected by the
// duplicate policy (the first one, or the first non-empty one when the first
// is empty), or nil when the stream holds none. The result aliases b.
//
// The read-path walkers (FindIPTC, IPTCAndDigest, FindDigest) loop over
// ParseEntry directly instead of using scanner: returning the multi-word
// resource struct from a function called per resource measurably slowed
// JPEG extraction on arm64 (BenchmarkJPEGExtract, +10%).
func FindIPTC(b []byte) []byte {
	_, data, _ := iptcWinner(b)
	return data
}

// IPTCAndDigest scans b for the IPTC-NAA resource (0x0404) and the IPTC
// digest resource (0x0425). Either result may be nil. Duplicates are resolved
// by the duplicate policy (see iptcWinner): the first 0x0404 resource wins
// unless it is empty and a later one is not, and the first 0x0425 resource of
// exactly 16 bytes wins; a 0x0425 resource of any other size is ignored (MWG
// Guidelines v2.0 §3.3.1). The results alias b.
//
// Task #313: this function used to keep the last copy of each resource while
// FindIPTC kept the first, so a stream with two 0x0404 resources read
// differently through the JPEG and the TIFF paths.
func IPTCAndDigest(b []byte) (iptcData, digest []byte) {
	pos := 0
	haveIPTC := false
	for pos < len(b) {
		id, data, newPos, ok := ParseEntry(b, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		switch {
		case id == ResourceIPTC && preferIPTC(haveIPTC, iptcData, data):
			iptcData, haveIPTC = data, true
		case id == ResourceIPTCDigest && digest == nil && len(data) == DigestSize:
			digest = data
		}
		pos = next(b, newPos, len(data))
	}
	return iptcData, digest
}

// FindDigest returns the data of the first 0x0425 resource of b that is
// exactly DigestSize bytes long, or nil. The result aliases b.
func FindDigest(b []byte) []byte {
	pos := 0
	for pos < len(b) {
		id, data, newPos, ok := ParseEntry(b, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		if id == ResourceIPTCDigest && len(data) == DigestSize {
			return data
		}
		pos = next(b, newPos, len(data))
	}
	return nil
}

// Digest returns the IPTC digest of iim (MWG Guidelines v2.0 §4.2.3.2: the MD5
// of the IPTC-NAA block), computed over iim followed by zero bytes up to the
// next multiple of align. align 1 hashes iim as is (JPEG resource 0x0404);
// align 4 hashes the value of a TIFF tag 0x83BB of type LONG as it is stored,
// zero padding included, which is what Photoshop and ExifTool hash.
//
// Derived from ReconcileIPTC.cpp:ComputeIPTCDigest (MD5 over the full block).
func Digest(iim []byte, align int) [DigestSize]byte {
	pad := 0
	if align > 1 {
		pad = (align - len(iim)%align) % align
	}
	if pad == 0 {
		return md5.Sum(iim) //nolint:gosec // G401: see import
	}
	h := md5.New() //nolint:gosec // G401: see import
	_, _ = h.Write(iim)
	var zeros [8]byte
	_, _ = h.Write(zeros[:pad])
	var out [DigestSize]byte
	h.Sum(out[:0])
	return out
}

// DigestCurrent reports whether digest is the IPTC digest of iim, either
// computed over iim alone or over iim padded to a multiple of align. The
// second form accepts digests of a TIFF tag 0x83BB of type LONG including its
// zero padding; derived from TIFF_Handler.cpp:TIFF_MetaHandler::ProcessXMP,
// which rechecks the digest without the trailing padding ("Older versions of
// Photoshop wrote tag 33723 with type LONG, but ignored the trailing zero
// padding for the IPTC digest").
func DigestCurrent(iim, digest []byte, align int) bool {
	if len(digest) != DigestSize {
		return false
	}
	if d := Digest(iim, 1); bytes.Equal(d[:], digest) {
		return true
	}
	if align > 1 && len(iim)%align != 0 {
		d := Digest(iim, align)
		return bytes.Equal(d[:], digest)
	}
	return false
}

// RefreshDigest returns a copy of b in which the data of every 0x0425
// resource of exactly DigestSize bytes is replaced by digest, or nil when b
// holds no such resource. b itself is never modified, so it may alias a
// caller's input buffer.
func RefreshDigest(b []byte, digest [DigestSize]byte) []byte {
	if FindDigest(b) == nil {
		return nil
	}
	out := bytes.Clone(b)
	s := scanner{b: out}
	for r, ok := s.next(); ok; r, ok = s.next() {
		if r.id == ResourceIPTCDigest && len(r.data) == DigestSize {
			copy(r.data, digest[:])
		}
	}
	return out
}

// AppendBlock appends one resource with an empty Pascal name (length 0 plus
// its pad byte) and the given data, padded to even length, onto dst.
func AppendBlock(dst []byte, id uint16, data []byte) []byte {
	dst = append(dst, '8', 'B', 'I', 'M')
	dst = binary.BigEndian.AppendUint16(dst, id)
	dst = append(dst, 0x00, 0x00) // empty Pascal name + padding byte
	// Callers pass resource data read from, or destined for, a JPEG segment or
	// a TIFF tag value, both far below 4 GiB.
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data))) //nolint:gosec // G115: see comment above
	dst = append(dst, data...)
	if len(data)%2 != 0 {
		dst = append(dst, 0x00)
	}
	return dst
}

// DigestPlan is the digest decision of a splice.
type DigestPlan uint8

const (
	// DigestKeep copies every 0x0425 resource verbatim.
	DigestKeep DigestPlan = iota
	// DigestRefresh rewrites every 16-byte 0x0425 resource in place.
	DigestRefresh
	// DigestCreate appends a new 0x0425 resource (the stream holds none).
	DigestCreate
)

// splicedDigest decides, before the splice, what AppendSpliced does with the
// digest (the 0x0425 resource may precede 0x0404). With newIPTC nil nothing
// changes. Otherwise:
//   - orig holds no 16-byte digest: it is created when create is true (the
//     caller writes XMP as well; MWG Guidelines v2.0 §4.2.3.2, p. 29);
//   - orig holds a digest that matches the IIM it holds now: it is refreshed
//     to the MD5 of newIPTC, so it keeps describing the IIM that is written;
//   - orig holds a digest that does not match the IIM it holds (stale, or no
//     IIM to match): it is kept stale, because a stale digest is the signal
//     that the IIM was edited by a tool that ignored XMP (MWG §4.2.3.2,
//     scenario 4, p. 28) and refreshing it would erase that signal for every
//     property this write did not reconcile.
func splicedDigest(orig, newIPTC []byte, create bool) (digest [DigestSize]byte, plan DigestPlan) {
	if newIPTC == nil {
		return digest, DigestKeep
	}
	stored := FindDigest(orig)
	if stored == nil {
		if create {
			return Digest(newIPTC, 1), DigestCreate
		}
		return digest, DigestKeep
	}
	old := FindIPTC(orig)
	if old == nil || !DigestCurrent(old, stored, 1) {
		return digest, DigestKeep
	}
	digest = Digest(newIPTC, 1)
	if bytes.Equal(stored, digest[:]) {
		return digest, DigestKeep
	}
	return digest, DigestRefresh
}

// AppendSpliced appends to dst an IRB identical to orig except that the
// 0x0404 resource is replaced by a fresh block wrapping newIPTC; every other
// resource is copied verbatim, with its Pascal name, reserved bytes and
// padding. When newIPTC is nil every 0x0404 resource is removed instead. When
// orig holds no 0x0404 resource and newIPTC is non-nil the new block is
// appended at the end. Duplicate 0x0404 resources are resolved as described
// at iptcWinner: the new block takes the place of the selected copy and the
// other copies are dropped, as the XMP Toolkit's writer keeps one resource
// per ID (PSIR_FileWriter.cpp:PSIR_FileWriter::UpdateMemoryResources), so no
// stale copy of the IIM survives the write (task #313).
//
// Digest (tasks #284, #323; MWG Guidelines v2.0 §4.2.3.2, pp. 26-29: the
// digest is the MD5 of the whole IPTC-IIM block, stored in resource 1061, and
// a Creator or Changer that writes IPTC-IIM and XMP together MUST create it):
//   - createDigest true and no 16-byte 0x0425 resource: one is appended after
//     the IIM block, holding the MD5 of newIPTC. Callers pass true when XMP is
//     written or present as well; ReconcileIPTC.cpp:SetIPTCDigest creates the
//     resource unconditionally, which this package restricts to that case so
//     an IIM-only write does not claim an XMP that does not exist;
//   - an existing digest that matches the IIM already in orig is rewritten
//     with the MD5 of newIPTC;
//   - an existing digest that does not match it (stale) is left untouched, as
//     is every digest when newIPTC is nil. A stale digest is the signal MWG
//     reconciliation uses to detect IPTC edits made behind the back of XMP;
//     the ExifTool rule (MWG.pm:ReconcileIPTCDigest updates only an absent or
//     matching digest) agrees.
//
// Derived from ReconcileIPTC.cpp:SetIPTCDigest (MD5 over the full IPTC block).
func AppendSpliced(dst, orig, newIPTC []byte, createDigest bool) []byte {
	digest, plan := splicedDigest(orig, newIPTC, createDigest)
	if plan == DigestKeep {
		return AppendSplicedDigest(dst, orig, newIPTC, nil, false)
	}
	return AppendSplicedDigest(dst, orig, newIPTC, &digest, plan == DigestCreate)
}

// AppendSplicedDigest is AppendSpliced with the digest decision taken by the
// caller: when digest is non-nil, the data of every 0x0425 resource of
// exactly DigestSize bytes is replaced by *digest, and when none exists and
// create is true a new 0x0425 resource holding *digest is appended after the
// IIM block; when digest is nil, every 0x0425 resource is copied verbatim and
// create is ignored. The TIFF writer uses it because its digest covers the
// zero-padded value of tag 0x83BB (Digest with align 4), not the IIM alone.
func AppendSplicedDigest(dst, orig, newIPTC []byte, digest *[DigestSize]byte, create bool) []byte { //nolint:gocyclo // one pass over the resources with the three outcomes for 0x0404 and for 0x0425
	winStart, _, found := iptcWinner(orig)
	haveDigest := false
	s := scanner{b: orig}
	for r, ok := s.next(); ok; r, ok = s.next() {
		switch {
		case r.id == ResourceIPTC:
			if newIPTC != nil && r.start == winStart {
				dst = AppendBlock(dst, ResourceIPTC, newIPTC)
			}
		case r.id == ResourceIPTCDigest && len(r.data) == DigestSize:
			haveDigest = true
			if digest == nil {
				dst = append(dst, orig[r.start:r.end]...)
				continue
			}
			// Header up to the data (signature, ID, name, size) verbatim, then
			// the fresh digest; 16 bytes need no pad byte.
			dst = append(dst, orig[r.start:r.dataStart]...)
			dst = append(dst, digest[:]...)
		default:
			dst = append(dst, orig[r.start:r.end]...)
		}
	}
	if !found && newIPTC != nil {
		dst = AppendBlock(dst, ResourceIPTC, newIPTC)
	}
	if digest != nil && create && !haveDigest {
		dst = AppendBlock(dst, ResourceIPTCDigest, digest[:])
	}
	return dst
}
