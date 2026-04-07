package makernote

import "testing"

func TestDispatchUnknownMake(t *testing.T) {
	t.Parallel()
	p := Dispatch("UNKNOWN_BRAND")
	if p != nil {
		t.Errorf("expected nil parser for unknown make, got %T", p)
	}
}

func TestDispatchCanon(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	// "Nikon" (without "CORPORATION") should also match.
	p := Dispatch("Nikon")
	if p == nil {
		t.Fatal("expected non-nil parser for 'Nikon' alias")
	}
}

func TestDispatchFujifilm(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	for _, make := range []string{"PENTAX Corporation", "Ricoh", "RICOH"} {
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

func TestDispatchPanasonic(t *testing.T) {
	t.Parallel()
	p := Dispatch("Panasonic")
	if p == nil {
		t.Fatal("expected non-nil parser for Panasonic")
	}
	result, err := p.Parse([]byte("Panasonic\x00\x00\x00" + "short"))
	if err != nil {
		t.Errorf("Panasonic Parse returned error: %v", err)
	}
	// Too short to contain valid IFD.
	_ = result
}

func TestDispatchLeica(t *testing.T) {
	t.Parallel()
	for _, make := range []string{"LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica"} {
		p := Dispatch(make)
		if p == nil {
			t.Fatalf("expected non-nil parser for %q", make)
		}
		result, err := p.Parse([]byte{0x00, 0x00})
		if err != nil {
			t.Errorf("Leica Parse returned error: %v", err)
		}
		_ = result
	}
}

func TestDispatchDJI(t *testing.T) {
	t.Parallel()
	p := Dispatch("DJI")
	if p == nil {
		t.Fatal("expected non-nil parser for DJI")
	}
	result, err := p.Parse([]byte{0x00, 0x00})
	if err != nil {
		t.Errorf("DJI Parse returned error: %v", err)
	}
	_ = result
}

func TestDispatchSamsung(t *testing.T) {
	t.Parallel()
	p := Dispatch("SAMSUNG")
	if p == nil {
		t.Fatal("expected non-nil parser for SAMSUNG")
	}
	result, err := p.Parse([]byte{0x00, 0x00})
	if err != nil {
		t.Errorf("Samsung Parse returned error: %v", err)
	}
	_ = result
}

func TestDispatchSigma(t *testing.T) {
	t.Parallel()
	p := Dispatch("SIGMA")
	if p == nil {
		t.Fatal("expected non-nil parser for SIGMA")
	}
	result, err := p.Parse([]byte("SIGMA\x00\x00\x00short"))
	if err != nil {
		t.Errorf("Sigma Parse returned error: %v", err)
	}
	_ = result
}
