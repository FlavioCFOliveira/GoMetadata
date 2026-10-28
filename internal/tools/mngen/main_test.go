package main

import (
	"errors"
	"testing"
)

// sampleClass imitates the layout of a metadata-extractor directory class: top
// level constants, a nested class whose constants are expressions, a constant
// without a name, a duplicate ID and a constant in the Tag* style.
const sampleClass = `
public class FooMakernoteDirectory extends Directory
{
    public static final int TAG_LENS_MODEL = 0x0095;
    public static final int TAG_SERIAL = 0x000C;
    public static final int TAG_DECIMAL = 16;
    public static final int TAG_NO_NAME = 0x9050;
    public static final int TAG_DUPLICATE = 0x000C;
    public static final int TagCamelStyle = 0x0020;

    public final static class Nested
    {
        private static final int OFFSET = 0xC100;
        public static final int TAG_MACRO_MODE = OFFSET + 0x01;
        public static final int TAG_INDENTED = 0x0777;
    }

    static
    {
        _tagNameMap.put(TAG_LENS_MODEL, "Lens Model");
        _tagNameMap.put(TAG_SERIAL, "WB RGB Levels");
        _tagNameMap.put(TAG_DECIMAL, "Auto-focus (1)");
        _tagNameMap.put(TAG_DUPLICATE, "Other");
        _tagNameMap.put(TagCamelStyle, "Camel");
    }
}
`

func TestParseClass(t *testing.T) {
	t.Parallel()
	tags, dups, unnamed, err := parseClass(sampleClass)
	if err != nil {
		t.Fatal(err)
	}
	want := []tag{{0x000C, "WBRGBLevels"}, {0x0010, "AutoFocus1"}, {0x0020, "Camel"}, {0x0095, "LensModel"}}
	if len(tags) != len(want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Errorf("tag %d = %v, want %v", i, tags[i], want[i])
		}
	}
	if len(dups) != 1 {
		t.Errorf("dups = %v, want one", dups)
	}
	if len(unnamed) != 1 || unnamed[0] != "TAG_NO_NAME" {
		t.Errorf("unnamed = %v, want [TAG_NO_NAME]", unnamed)
	}
}

func TestParseClassErrors(t *testing.T) {
	t.Parallel()
	if _, _, _, err := parseClass("public class X {}"); !errors.Is(err, errNoTags) {
		t.Errorf("empty class: err = %v, want errNoTags", err)
	}
	if _, _, _, err := parseClass("    public static final int TAG_BIG = 0x10000;\n"); err == nil {
		t.Error("a constant above 16 bits must be rejected")
	}
}

func TestCamel(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Lens Model":         "LensModel",
		"WB RGB Levels":      "WBRGBLevels",
		"Auto-focus (1)":     "AutoFocus1",
		"ISO":                "ISO",
		"already camelCased": "AlreadyCamelCased",
		"":                   "",
	} {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExcludedNotGenerated: a class marked as excluded must never be in the
// list of generated tables.
func TestExcludedNotGenerated(t *testing.T) {
	t.Parallel()
	for _, tb := range tables {
		if why, bad := excluded[tb.class]; bad {
			t.Errorf("%s is generated but excluded: %s", tb.class, why)
		}
	}
	if len(excluded) == 0 {
		t.Error("the exclusion list must document the ExifTool-attributed classes")
	}
}
