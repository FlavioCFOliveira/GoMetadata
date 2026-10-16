package iptc

import "crypto/md5" //nolint:gosec // MWG §4.2.3.2 (p. 26) mandates MD5 for the IPTC digest; security is not the concern here

// Digest computes the MD5 hash of rawIIM as required by the Metadata Working
// Group (MWG) Guidelines v2.0 §4.2.3.2 (p. 26) for the Photoshop Image Resource
// 1061 (0x0425, "IPTC-NAA Digest"). The digest is computed over the raw byte
// stream of resource 0x0404, before any parsing. Per MWG §4.2.3.2, p. 26, the
// digest is the MD5 hash of the entire IPTC-IIM block, stored as 16 binary bytes.
func Digest(rawIIM []byte) [16]byte {
	//nolint:gosec // MD5 required by MWG §4.2.3.2, p. 26; not used for security
	return md5.Sum(rawIIM)
}

// DigestIsZero reports whether d is the all-zero "unknown" sentinel value.
// The sentinel is a convention of this library: MWG §4.2.3.2 (pp. 26-29)
// defines no such value. When the stored digest is all-zero the IPTC may have
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
//     (regardless of the rawIIM content); this library treats all-zero as
//     "digest unknown" and elevates IPTC trust in this case.
//
// MWG §4.2.3.2, pp. 27-28: match (or no digest) → XMP keeps read priority
// (scenario 3); mismatch → elevate IPTC trust for fields where IPTC and XMP
// differ (scenario 4, which MWG resolves per property); unknown is the
// library's own sentinel and is handled as a mismatch.
func DigestMatch(rawIIM []byte, stored [16]byte) (match, unknown bool) {
	if DigestIsZero(stored) {
		return false, true
	}
	computed := Digest(rawIIM)
	return computed == stored, false
}
