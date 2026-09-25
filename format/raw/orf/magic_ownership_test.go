package orf

import (
	"bytes"
	"testing"
)

// TestExtractKeepsOriginalMagic verifies that Extract returns rawEXIF with
// the original container magic at bytes [2:4] and does not copy the input.
func TestExtractKeepsOriginalMagic(t *testing.T) {
	t.Parallel()
	data := buildORF()
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF[:4], []byte{0x49, 0x49, 0x52, 0x4F}) {
		t.Fatalf("rawEXIF[0:4] = % X, want % X", rawEXIF[:4], []byte{0x49, 0x49, 0x52, 0x4F})
	}
	if !bytes.Equal(rawEXIF, data) {
		t.Fatal("rawEXIF differs from the input bytes")
	}
}

// TestInjectRestoresMagicWithoutMutatingRawEXIF verifies that Inject writes
// the container magic at bytes [2:4] of the output and leaves the caller's
// rawEXIF unchanged.
func TestInjectRestoresMagicWithoutMutatingRawEXIF(t *testing.T) {
	t.Parallel()
	data := buildORF()
	rawEXIF := bytes.Clone(data)
	rawEXIF[2], rawEXIF[3] = 0x2A, 0x00 // standard TIFF magic, caller-owned
	orig := bytes.Clone(rawEXIF)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, rawEXIF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, orig) {
		t.Fatal("Inject modified the caller's rawEXIF")
	}
	want := bytes.Clone(orig)
	copy(want[:4], []byte{0x49, 0x49, 0x52, 0x4F})
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatal("Inject output differs from rawEXIF with the container magic restored")
	}
}
