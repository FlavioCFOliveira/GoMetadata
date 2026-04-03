package makernote

import "testing"

func TestDispatchUnknownMake(t *testing.T) {
	p := Dispatch("UNKNOWN_BRAND")
	if p != nil {
		t.Errorf("expected nil parser for unknown make, got %T", p)
	}
}

func TestDispatchCanon(t *testing.T) {
	p := Dispatch("Canon")
	if p == nil {
		t.Fatal("expected non-nil parser for Canon")
	}
	// Canon parser stub must not panic; returns nil, nil.
	result, err := p.Parse([]byte{0x00, 0x01, 0x00, 0x00})
	if err != nil {
		t.Errorf("Canon Parse returned error: %v", err)
	}
	if result != nil {
		t.Errorf("Canon Parse returned non-nil result for stub: %v", result)
	}
}

func TestDispatchNikon(t *testing.T) {
	p := Dispatch("NIKON CORPORATION")
	if p == nil {
		t.Fatal("expected non-nil parser for Nikon")
	}
	result, err := p.Parse([]byte{0x4E, 0x69, 0x6B, 0x6F})
	if err != nil {
		t.Errorf("Nikon Parse returned error: %v", err)
	}
	if result != nil {
		t.Errorf("Nikon Parse returned non-nil result for stub: %v", result)
	}
}

func TestDispatchSony(t *testing.T) {
	p := Dispatch("SONY")
	if p == nil {
		t.Fatal("expected non-nil parser for Sony")
	}
	result, err := p.Parse([]byte{0x53, 0x4F, 0x4E, 0x59})
	if err != nil {
		t.Errorf("Sony Parse returned error: %v", err)
	}
	if result != nil {
		t.Errorf("Sony Parse returned non-nil result for stub: %v", result)
	}
}

func TestDispatchNikonAlias(t *testing.T) {
	// "Nikon" (without "CORPORATION") should also match.
	p := Dispatch("Nikon")
	if p == nil {
		t.Fatal("expected non-nil parser for 'Nikon' alias")
	}
}

func TestDispatchFujifilm(t *testing.T) {
	p := Dispatch("FUJIFILM")
	if p == nil {
		t.Fatal("expected non-nil parser for FUJIFILM")
	}
	// Minimal payload: too short to parse, must return nil map without panicking.
	result, err := p.Parse([]byte{0x46, 0x55, 0x4A, 0x49})
	if err == nil && result != nil {
		// A non-nil map from a 4-byte payload would be unexpected.
		t.Errorf("FUJIFILM Parse returned non-nil result for stub payload: %v", result)
	}
}

func TestDispatchOlympus(t *testing.T) {
	for _, make := range []string{"OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus"} {
		p := Dispatch(make)
		if p == nil {
			t.Fatalf("expected non-nil parser for %q", make)
		}
		// Too-short payload: must not panic and return nil map.
		result, err := p.Parse([]byte{0x4F, 0x4C, 0x59, 0x4D})
		if err != nil {
			t.Errorf("Olympus Parse returned error for stub: %v", err)
		}
		if result != nil {
			t.Errorf("Olympus Parse returned non-nil result for stub: %v", result)
		}
	}
}

func TestDispatchPentax(t *testing.T) {
	for _, make := range []string{"PENTAX Corporation", "Ricoh"} {
		p := Dispatch(make)
		if p == nil {
			t.Fatalf("expected non-nil parser for %q", make)
		}
		// Too-short payload: must not panic and return nil map.
		result, err := p.Parse([]byte{0x41, 0x4F, 0x43, 0x00})
		if err != nil {
			t.Errorf("Pentax Parse returned error for stub: %v", err)
		}
		if result != nil {
			t.Errorf("Pentax Parse returned non-nil result for stub: %v", result)
		}
	}
}
