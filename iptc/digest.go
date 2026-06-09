package iptc

import "crypto/md5" //nolint:gosec // MWG §3.3.1 mandates MD5 for the IPTC digest; security is not the concern here

// Digest computes the MD5 hash of rawIIM as required by the Metadata Working
// Group (MWG) Guidelines v2.0 §3.3.1 for the Photoshop Image Resource Block
// 0x0425 ("IPTC-NAA Digest"). The digest is computed over the raw byte stream
// of resource 0x0404, before any parsing. Per MWG §3.3.1, the digest is the
// MD5 checksum of the complete IPTC-NAA dataset stream.
func Digest(rawIIM []byte) [16]byte {
	//nolint:gosec // MD5 required by MWG §3.3.1; not used for security
	return md5.Sum(rawIIM)
}

// DigestIsZero reports whether d is the all-zero "unknown" sentinel value
// defined by MWG §3.3.1. When the stored digest is all-zero the IPTC may have
// been edited without updating the digest, so callers should treat this the
// same as a digest mismatch (elevate IPTC trust).
func DigestIsZero(d [16]byte) bool {
	return d == [16]byte{}
}

// DigestMatch computes the MD5 of rawIIM and compares it to stored.
// Returns (match, unknown) where:
//   - match is true when the computed digest equals the stored digest AND the
//     stored digest is not the all-zero sentinel.
//   - unknown is true when the stored digest is the all-zero sentinel
//     (regardless of the rawIIM content); MWG §3.3.1 treats all-zero as
//     "digest unknown" and mandates IPTC elevation in this case.
//
// MWG §3.3.1: match → XMP keeps read priority (existing default);
// mismatch or unknown → elevate IPTC trust for fields where IPTC and XMP differ.
func DigestMatch(rawIIM []byte, stored [16]byte) (match, unknown bool) {
	if DigestIsZero(stored) {
		return false, true
	}
	computed := Digest(rawIIM)
	return computed == stored, false
}
