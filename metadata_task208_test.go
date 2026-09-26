package gometadata

// metadata_task208_test.go — regression gate for task #208 (Performance and
// Efficiency Laboratory sprint 44): iptcTrustElevated is computed exactly
// once, by Read, and cached on Metadata, instead of recomputing an MD5
// digest of the raw IPTC stream on every MWG-02 accessor call.

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// TestIPTCTrustElevatedCachedSingleMD5 proves that computeIPTCTrustElevated's
// underlying MD5 computation (iptc.DigestMatch, reached here via the
// digestMatchFn seam) runs at most once per Read call, regardless of how many
// times the four MWG-02 digest-conditioned accessors (Copyright, Caption,
// Keywords, Creator) are subsequently called.
//
// The scenario uses a digest MISMATCH (task #208's worst case): before the
// fix, every accessor call independently reached iptc.DigestMatch because
// iptcTrustElevated() recomputed the comparison from scratch each time.
func TestIPTCTrustElevatedCachedSingleMD5(t *testing.T) { //nolint:paralleltest // reassigns the package-level digestMatchFn seam; must not run concurrently with other tests exercising Read/accessors
	// Deliberately wrong digest so DigestMatch's real comparison always runs
	// (the all-zero sentinel short-circuits differently and is covered by
	// TestConformance_MWG02/zero-digest-sentinel-iptc-wins already).
	wrongDigest := make([]byte, 16)
	for idx := range wrongDigest {
		wrongDigest[idx] = 0xAB
	}
	jpegBytes := buildMWG02JPEG("IPTC caption", "XMP caption", wrongDigest)

	orig := digestMatchFn
	var calls int32
	digestMatchFn = func(rawIIM []byte, stored [16]byte) (match, unknown bool) {
		atomic.AddInt32(&calls, 1)
		return orig(rawIIM, stored)
	}
	t.Cleanup(func() { digestMatchFn = orig })

	m, err := Read(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("digestMatchFn calls after Read = %d, want exactly 1 (Read must compute the digest decision eagerly)", got)
	}

	// Call all four MWG-02 accessors, several times each. None of them must
	// trigger another digestMatchFn call: the decision is cached.
	// buildMWG02JPEG only populates the Caption field (IPTC 2:120 / XMP
	// dc:description); Copyright/Keywords/Creator are absent from both
	// sources and correctly return their zero values regardless of which
	// source wins — this test only cares about the digestMatchFn call count.
	for range 5 {
		if got := m.Caption(); got != "IPTC caption" {
			t.Errorf("Caption() = %q, want %q (digest mismatch → IPTC priority)", got, "IPTC caption")
		}
		_ = m.Copyright()
		_ = m.Keywords()
		_ = m.Creator()
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("digestMatchFn calls after 5x{Copyright,Caption,Keywords,Creator} = %d, want exactly 1 (cached at Read time, task #208)", got)
	}
}

// TestIPTCTrustElevatedDefaultForNonReadConstructed verifies that a Metadata
// not produced by Read (NewMetadata, or a bare struct literal) keeps the
// correct default (false — MWG-01 default XMP priority) for iptcTrustElev,
// since rawIPTCDigest is nil in that case and computeIPTCTrustElevated's own
// nil-digest fast path returns false without needing to run.
func TestIPTCTrustElevatedDefaultForNonReadConstructed(t *testing.T) {
	t.Parallel()
	m := NewMetadata(format.FormatJPEG)
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("only IPTC caption")
	if got := m.iptcTrustElevated(); got {
		t.Errorf("iptcTrustElevated() = true on a NewMetadata-constructed value, want false (no digest resource -> default MWG-01)")
	}
	if got := m.Caption(); got != "only IPTC caption" {
		t.Errorf("Caption() = %q, want %q", got, "only IPTC caption")
	}
}
