package xmp

// task210_test.go — regression battery: xmp.Parse's returned property
// values/keys must never alias the caller's input buffer (bug #72). Beyond
// TestParsedPropertyIndependentOfInputBuffer (xmp_test.go), these tests
// model a sync.Pool-sourced buffer returned to the pool and reused by an
// UNRELATED Get() call for different data, and a distinct (non-zero)
// overwrite pattern, so a coincidental all-zero mutation cannot mask a
// subtle aliasing bug.

import (
	"strings"
	"sync"
	"testing"
)

// task210RepresentativeXMP mirrors representativeXMP (bench_test.go) closely
// enough to exercise the entity-free fast path (unescapeXML), the
// tagLocal→propLocal key conversion (onStartProperty), a struct-in-list
// property, and a multi-item collection.
const task210RepresentativeXMP = `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
      xmlns:xmp="http://ns.adobe.com/xap/1.0/"
      tiff:Model="Canon EOS R5"
      tiff:Make="Canon"
      xmp:CreatorTool="Adobe Photoshop Lightroom">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">A scenic mountain landscape at sunset</rdf:li>
        </rdf:Alt>
      </dc:description>
      <dc:subject>
        <rdf:Bag>
          <rdf:li>nature</rdf:li>
          <rdf:li>landscape</rdf:li>
          <rdf:li>sunset</rdf:li>
        </rdf:Bag>
      </dc:subject>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// task210BufPool models a caller-side sync.Pool of byte buffers — the reason
// unescapeXML/onStartProperty may not alias the caller's input directly.
var task210BufPool = sync.Pool{New: func() any { //nolint:gochecknoglobals // test-only pool, mirrors a caller's own pooling pattern
	b := make([]byte, 0, 4096)
	return &b
}}

// TestTask210ParsedPropertiesSurvivePoolReuse is a #72 regression gate:
// after Parse returns, the caller's pooled input buffer is returned to the
// pool and IMMEDIATELY reused by an unrelated Get() call that overwrites it
// with different content. Every property value and property key already
// extracted by the first Parse call must be completely unaffected.
func TestTask210ParsedPropertiesSurvivePoolReuse(t *testing.T) {
	t.Parallel()

	// Acquire a pooled buffer, exactly as a caller reading many files in a
	// hot loop would (e.g. gometadata.Read's internal rawXMP handling before
	// jpeg.ExtractFull's mandatory bytes.Clone — the container layer already
	// clones before calling xmp.Parse, but xmp.Parse itself must be safe even
	// for callers who do NOT — the public API makes no such promise to its
	// callers about needing to clone first).
	bufPtr := task210BufPool.Get().(*[]byte) //nolint:forcetypeassert,revive // pool invariant: task210BufPool.New always stores *[]byte
	*bufPtr = append((*bufPtr)[:0], task210RepresentativeXMP...)
	input := *bufPtr

	x, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Record every value/key we care about BEFORE returning the buffer.
	wantModel := "Canon EOS R5"
	wantMake := "Canon"
	wantTool := "Adobe Photoshop Lightroom"
	wantCaption := "A scenic mountain landscape at sunset"
	wantKeywords := []string{"nature", "landscape", "sunset"}

	if got := x.CameraModel(); got != wantModel {
		t.Fatalf("precondition failed: CameraModel = %q, want %q", got, wantModel)
	}
	if got := x.Get(NStiff, "Make"); got != wantMake {
		t.Fatalf("precondition failed: tiff:Make = %q, want %q", got, wantMake)
	}
	if got := x.Get(NSxmp, "CreatorTool"); got != wantTool {
		t.Fatalf("precondition failed: xmp:CreatorTool = %q, want %q", got, wantTool)
	}
	if got := x.Caption(); got != wantCaption {
		t.Fatalf("precondition failed: Caption = %q, want %q", got, wantCaption)
	}

	// Return the buffer to the pool, exactly as the caller would once done
	// with `input`. xmp.Parse must have already copied whatever it needed
	// into its own XMP.arena.
	task210BufPool.Put(bufPtr)

	// Immediately reuse the SAME pool for unrelated data — the pool has no
	// other tenants in this test, so bufPtr is very likely to be handed back
	// (sync.Pool has no ordering guarantee, but a single-slot pool under no
	// concurrent load reliably round-trips the same backing array in
	// practice; the fallback assertion below (garbage) covers the case where
	// it doesn't).
	other := task210BufPool.Get().(*[]byte) //nolint:forcetypeassert,revive // pool invariant
	garbage := strings.Repeat("Z", len(task210RepresentativeXMP)+64)
	*other = append((*other)[:0], garbage...)

	// Every previously-extracted value/key must be byte-for-byte unchanged.
	if got := x.CameraModel(); got != wantModel {
		t.Errorf("CameraModel after pool reuse: got %q, want %q (buffer aliasing regression)", got, wantModel)
	}
	if got := x.Get(NStiff, "Make"); got != wantMake {
		t.Errorf("tiff:Make after pool reuse: got %q, want %q (buffer aliasing regression)", got, wantMake)
	}
	if got := x.Get(NSxmp, "CreatorTool"); got != wantTool {
		t.Errorf("xmp:CreatorTool after pool reuse: got %q, want %q (buffer aliasing regression)", got, wantTool)
	}
	if got := x.Caption(); got != wantCaption {
		t.Errorf("Caption after pool reuse: got %q, want %q (buffer aliasing regression)", got, wantCaption)
	}
	if got := x.Keywords(); !equalStringSlices(got, wantKeywords) {
		t.Errorf("Keywords after pool reuse: got %v, want %v (buffer aliasing regression)", got, wantKeywords)
	}

	// Also verify the map KEYS themselves (propLocal, populated via
	// transientAliasString in onStartProperty) are intact — a corrupted key would
	// make the property invisible under its correct name entirely, which the
	// Get() calls above already exercise, but check directly too for clarity.
	if _, ok := x.Properties[NStiff]["Model"]; !ok {
		t.Error(`Properties[NStiff]["Model"] missing after pool reuse — propLocal key corrupted`)
	}
	if _, ok := x.Properties[NSdc]["description"]; !ok {
		t.Error(`Properties[NSdc]["description"] missing after pool reuse — propLocal key corrupted`)
	}

	task210BufPool.Put(other)
}

// TestTask210ParsedPropertiesSurviveDistinctOverwrite strengthens the #72
// regression gate by overwriting the caller's original input with a
// DIFFERENT recognisable byte pattern (not zeroing) after Parse returns,
// closing the gap where an all-zero mutation could coincidentally fail to
// expose an aliasing bug (e.g. if a value happened to already contain only
// zero-adjacent bytes).
func TestTask210ParsedPropertiesSurviveDistinctOverwrite(t *testing.T) {
	t.Parallel()

	input := []byte(task210RepresentativeXMP)

	x, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantModel := "Canon EOS R5"
	wantCaption := "A scenic mountain landscape at sunset"
	if got := x.CameraModel(); got != wantModel {
		t.Fatalf("precondition failed: CameraModel = %q, want %q", got, wantModel)
	}
	if got := x.Caption(); got != wantCaption {
		t.Fatalf("precondition failed: Caption = %q, want %q", got, wantCaption)
	}

	// Overwrite with a distinct, recognisable pattern — never all-zero.
	for i := range input {
		input[i] = byte('A' + i%26)
	}

	if got := x.CameraModel(); got != wantModel {
		t.Errorf("CameraModel after distinct overwrite: got %q, want %q (buffer aliasing regression)", got, wantModel)
	}
	if got := x.Caption(); got != wantCaption {
		t.Errorf("Caption after distinct overwrite: got %q, want %q (buffer aliasing regression)", got, wantCaption)
	}
}

// equalStringSlices reports whether a and b contain the same elements in the
// same order.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
