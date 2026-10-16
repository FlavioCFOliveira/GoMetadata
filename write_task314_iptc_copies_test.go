package gometadata

// write_task314_iptc_copies_test.go — task #314: an IPTC write updates the
// secondary IPTC copies (the 0x0404 resource inside TIFF tag 0x8649, and a
// PNG "Raw profile type iptc" chunk), so no reader sees a stale copy
// (ExifTool group IPTC2).

import (
	"bytes"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/png"
	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// TestWrite_TIFFPhotoshopIPTCCopyUpdated (task #314): after a caption write,
// tag 0x83BB and the 0x0404 copy in tag 0x8649 hold the same, new IIM; the
// other resources of the block are kept.
func TestWrite_TIFFPhotoshopIPTCCopyUpdated(t *testing.T) {
	t.Parallel()
	old := captionIIM(t, "before")
	thumb := irb.AppendBlock(nil, 0x040C, []byte("thumbnail"))
	src := tiffWithPSIR(t, old, append(bytes.Clone(thumb), irb.AppendBlock(nil, irb.ResourceIPTC, old)...))

	out := writeCaption(t, src, "after")
	e, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	native := e.IFD0.Get(exif.TagIPTC)
	psir := e.IFD0.Get(exif.TagID(tiffscan.PSIRTag))
	if native == nil || psir == nil {
		t.Fatal("output lost tag 0x83BB or 0x8649")
	}
	copyIIM := irb.FindIPTC(psir.Value)
	if !bytes.Equal(copyIIM, tiffscan.TrimIPTCLongPadding(native.Value)) {
		t.Errorf("0x8649 IIM copy %x differs from tag 0x83BB %x", copyIIM, native.Value)
	}
	parsed, err := iptc.Parse(copyIIM)
	if err != nil || parsed.Caption() != "after" {
		t.Errorf("0x8649 copy caption = %v (%v), want \"after\"", parsed, err)
	}
	if !bytes.Contains(psir.Value, thumb) {
		t.Error("the 0x040C resource of tag 0x8649 was not kept")
	}
}

// TestWrite_PNGRawProfileIPTCUpdated (task #314): the committed fixture holds
// IPTC only in a zTXt "Raw profile type iptc" chunk written by ExifTool 13.55;
// after a caption write the chunk holds the new caption and keeps the other
// datasets, and ExifTool reports a single, current IPTC group.
func TestWrite_PNGRawProfileIPTCUpdated(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/fixtures/png_raw_profile_iptc.png")
	if err != nil {
		t.Fatal(err)
	}
	out := writeCaption(t, src, "after")
	_, rawIPTC, _, err := png.Extract(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("png.Extract: %v", err)
	}
	i, err := iptc.Parse(rawIPTC)
	if err != nil {
		t.Fatalf("iptc.Parse: %v", err)
	}
	if i.Caption() != "after" {
		t.Errorf("raw profile caption = %q, want \"after\"", i.Caption())
	}
	if kw := i.Keywords(); len(kw) != 2 || kw[0] != "alpha" || kw[1] != "beta" {
		t.Errorf("keywords = %q, want [alpha beta]", kw)
	}
	if n := bytes.Count(out, []byte("Raw profile type iptc")); n != 1 {
		t.Errorf("%d raw profile chunks, want 1", n)
	}
}
