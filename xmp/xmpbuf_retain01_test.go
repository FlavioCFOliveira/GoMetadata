package xmp

// xmpbuf_retain01_test.go — permanent regression battery for XMPBUF-RETAIN-01:
// a parsed *XMP must retain memory proportional to its actually-stored
// content, not to the size of the source document. XMP.arena is a compact,
// append-only buffer that receives a copy of a value/key ONLY at the moment
// storeProperty or recordContainerType decides to keep it (see (*XMP).intern
// in rdf.go). Content that is scanned but never stored — comments,
// whitespace, dropped attribute values on attributes that end up unused
// (e.g. a huge rdf:about) — is read directly from the transient,
// synchronous-parse-only input buffer (see transientAliasString's contract)
// and becomes unreachable garbage the instant Parse returns, never touching
// arena at all.

import (
	"runtime"
	"strings"
	"testing"
)

// buildPaddedXMPCommentFiller constructs an XMP document containing a single
// small real property (tiff:Model) and a large XML comment as filler.
// paddingBytes controls the filler size.
func buildPaddedXMPCommentFiller(paddingBytes int) []byte {
	var b strings.Builder
	b.WriteString(`<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>` + "\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n")
	b.WriteString(`  <!-- `)
	for range paddingBytes / 4 {
		b.WriteString("PAD ")
	}
	b.WriteString(` -->` + "\n")
	b.WriteString(`  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")
	b.WriteString(`    <rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="Canon EOS R5"/>` + "\n")
	b.WriteString(`  </rdf:RDF>` + "\n")
	b.WriteString(`</x:xmpmeta>` + "\n")
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}

// buildPaddedXMPDroppedAttrFiller constructs an XMP document whose padding is
// a large VALUE on an attribute that is always dropped (rdf:about on the
// top-level rdf:Description) rather than a comment: the padded value IS run
// through unescapeXML (unlike a comment, which the tokenizer skips entirely
// without unescaping), but is never interned because onStartTopLevelDesc
// drops any attribute whose namespace is NSrdf before it would ever reach
// storeProperty.
func buildPaddedXMPDroppedAttrFiller(paddingBytes int) []byte {
	var b strings.Builder
	b.WriteString(`<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>` + "\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n")
	b.WriteString(`  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")
	b.WriteString(`    <rdf:Description rdf:about="`)
	b.WriteString(strings.Repeat("X", paddingBytes))
	b.WriteString(`" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="Canon EOS R5"/>` + "\n")
	b.WriteString(`  </rdf:RDF>` + "\n")
	b.WriteString(`</x:xmpmeta>` + "\n")
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}

// heapAlloc forces two full GC cycles (to settle any pending finalizers /
// sweep-triggered frees) and returns runtime.MemStats.HeapAlloc.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// testRetentionBounded is the shared assertion body for both filler vectors:
// parse numDocs copies of a padded document (paddingBytes filler each),
// keeping every resulting *XMP reachable, and assert the total heap growth
// stays far below a single document's own padding size — the OLD (buggy)
// behaviour would grow by approximately numDocs * paddingBytes (every parsed
// object pinning its own full padded body), so a growth bound of one
// paddingBytes-sized document, for ANY numDocs > 1, already gives a large,
// unambiguous separation margin between "fixed" and "regressed".
func testRetentionBounded(t *testing.T, name string, build func(int) []byte) {
	t.Helper()

	const paddingBytes = 4 << 20 // 4 MiB filler per document
	const numDocs = 10

	template := build(paddingBytes)
	if len(template) > maxXMPDocumentBytes {
		t.Fatalf("%s: test document (%d bytes) exceeds maxXMPDocumentBytes (%d) — reduce paddingBytes", name, len(template), maxXMPDocumentBytes)
	}

	// Baseline measured BEFORE any per-document input buffer is allocated.
	before := heapAlloc()

	// Each iteration allocates its OWN independent copy of the padded
	// document — exactly like N distinct uploaded files, each in its own
	// backing array — rather than reusing a single shared slice across all
	// Parse calls. `input` is loop-scoped and referenced nowhere else, so it
	// becomes unreachable at the end of each iteration UNLESS Parse's result
	// still aliases it — which is exactly the condition this test detects:
	// under the bug, docs[i].Properties (etc.) alias directly into `input`'s
	// backing array, keeping all numDocs independent ~paddingBytes arrays
	// alive; under the fix, only the actually-stored bytes are copied into
	// XMP.arena, so each `input` is collectible the moment this loop moves on.
	docs := make([]*XMP, 0, numDocs)
	for i := range numDocs {
		input := append([]byte(nil), template...)
		x, err := Parse(input)
		if err != nil {
			t.Fatalf("%s: Parse(doc %d): %v", name, i, err)
		}
		if got := x.CameraModel(); got != "Canon EOS R5" {
			t.Fatalf("%s: Parse(doc %d).CameraModel() = %q, want %q", name, i, got, "Canon EOS R5")
		}
		docs = append(docs, x)
	}

	after := heapAlloc()

	var grown uint64
	if after > before {
		grown = after - before
	}

	// The regression gate: total retained growth for ALL numDocs retained
	// objects must stay well under the size of a SINGLE document's padding.
	// The buggy behaviour would retain roughly numDocs * paddingBytes here
	// (40 MiB for these parameters); the fixed behaviour retains only the
	// actual stored content (a namespace URI + "Model" + "Canon EOS R5" plus
	// small map/struct/arena overhead per document — on the order of a few
	// hundred bytes each, a few KiB total for all numDocs).
	if grown >= paddingBytes {
		t.Errorf("%s: heap grew by %d bytes for %d retained documents (%d bytes of padding each) — "+
			"want growth well under a single document's padding size (%d bytes); "+
			"this indicates XMPBUF-RETAIN-01 has regressed (whole-document retention)",
			name, grown, numDocs, paddingBytes, paddingBytes)
	}
	t.Logf("%s: heap grew by %d bytes for %d retained documents (%d bytes of padding each, %d bytes total padding) — bound was < %d bytes",
		name, grown, numDocs, paddingBytes, numDocs*paddingBytes, paddingBytes)

	runtime.KeepAlive(docs)
}

// TestXMPBufRetain01CommentFiller is the primary regression gate, using a
// large XML comment as the padding vector.
//
//nolint:paralleltest // this test measures GLOBAL process heap stats (runtime.ReadMemStats); running concurrently with other t.Parallel() tests would let their unrelated allocations pollute the before/after delta, making the measurement meaningless
func TestXMPBufRetain01CommentFiller(t *testing.T) {
	testRetentionBounded(t, "comment-filler", buildPaddedXMPCommentFiller)
}

// TestXMPBufRetain01DroppedAttrFiller uses a large VALUE on an attribute
// that unescapeXML decodes but that is always dropped before ever reaching
// storeProperty (rdf:about on the top-level rdf:Description) as the padding
// vector.
//
//nolint:paralleltest // see TestXMPBufRetain01CommentFiller — global heap-stats measurement, must not race with other parallel tests' allocations
func TestXMPBufRetain01DroppedAttrFiller(t *testing.T) {
	testRetentionBounded(t, "dropped-attr-filler", buildPaddedXMPDroppedAttrFiller)
}

// TestXMPBufRetain01SanityDocumentIsActuallyPadded is a meta-test: it fails
// loudly (rather than the main tests silently passing for the wrong reason)
// if buildPaddedXMP* ever stops producing a document that is actually large,
// which would make the retention assertions above vacuous.
func TestXMPBufRetain01SanityDocumentIsActuallyPadded(t *testing.T) {
	t.Parallel()
	const paddingBytes = 4 << 20
	for name, build := range map[string]func(int) []byte{
		"comment-filler":      buildPaddedXMPCommentFiller,
		"dropped-attr-filler": buildPaddedXMPDroppedAttrFiller,
	} {
		doc := build(paddingBytes)
		if len(doc) < paddingBytes {
			t.Errorf("%s: document is only %d bytes, want >= %d (padding not effective)", name, len(doc), paddingBytes)
		}
		x, err := Parse(doc)
		if err != nil {
			t.Fatalf("%s: Parse: %v", name, err)
		}
		if got := x.CameraModel(); got != "Canon EOS R5" {
			t.Errorf("%s: CameraModel() = %q, want %q", name, got, "Canon EOS R5")
		}
	}
}
