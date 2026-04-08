package gometadata

import (
	"testing"
)

// TestReadOptions exercises each ReadOption constructor, verifying that the
// option correctly mutates a readConfig.
func TestReadOptions(t *testing.T) {
	t.Parallel()
	t.Run("WithoutXMP", func(t *testing.T) {
		t.Parallel()
		var c readConfig
		WithoutXMP()(&c)
		if !c.lazyXMP {
			t.Error("WithoutXMP: lazyXMP should be true")
		}
	})
	t.Run("WithoutIPTC", func(t *testing.T) {
		t.Parallel()
		var c readConfig
		WithoutIPTC()(&c)
		if !c.lazyIPTC {
			t.Error("WithoutIPTC: lazyIPTC should be true")
		}
	})
	t.Run("WithoutEXIF", func(t *testing.T) {
		t.Parallel()
		var c readConfig
		WithoutEXIF()(&c)
		if !c.lazyEXIF {
			t.Error("WithoutEXIF: lazyEXIF should be true")
		}
	})
	t.Run("WithoutMakerNote", func(t *testing.T) {
		t.Parallel()
		var c readConfig
		WithoutMakerNote()(&c)
		if !c.skipMakerNote {
			t.Error("WithoutMakerNote: skipMakerNote should be true")
		}
	})
}

// TestWriteOptions exercises PreserveUnknownSegments.
func TestWriteOptions(t *testing.T) {
	t.Parallel()
	t.Run("PreserveUnknownSegments_true", func(t *testing.T) {
		t.Parallel()
		var c writeConfig
		PreserveUnknownSegments(true)(&c)
		if !c.preserveUnknownSegments {
			t.Error("PreserveUnknownSegments(true): field should be true")
		}
	})
	t.Run("PreserveUnknownSegments_false", func(t *testing.T) {
		t.Parallel()
		var c writeConfig
		c.preserveUnknownSegments = true // set first
		PreserveUnknownSegments(false)(&c)
		if c.preserveUnknownSegments {
			t.Error("PreserveUnknownSegments(false): field should be false")
		}
	})
}
