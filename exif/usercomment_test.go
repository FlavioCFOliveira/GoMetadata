package exif

import (
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// Helper: build a raw UserComment field value.
//
// EXIF 2.32, CIPA DC-008-2023 §4.6.5 Table 4:
//   Layout: [8-byte charset prefix][text bytes]
// ---------------------------------------------------------------------------

func makeUserComment(prefix [8]byte, text []byte) []byte {
	b := make([]byte, userCommentPrefixLen+len(text))
	copy(b[:userCommentPrefixLen], prefix[:])
	copy(b[userCommentPrefixLen:], text)
	return b
}

// makeUTF16LE encodes a Go string as a null-terminated UTF-16 LE byte slice.
func makeUTF16LE(s string) []byte {
	runes := []rune(s)
	b := make([]byte, (len(runes)+1)*2) // +1 for null terminator
	for i, r := range runes {
		b[i*2] = byte(r)        //nolint:gosec // G115: intentional rune→byte truncation for UTF-16 LE low byte
		b[i*2+1] = byte(r >> 8) //nolint:gosec // G115: intentional rune→byte truncation for UTF-16 LE high byte
	}
	// b[(len(runes))*2] and b[(len(runes))*2+1] are already 0x00.
	return b
}

// makeUTF16BE encodes a Go string as a null-terminated UTF-16 BE byte slice.
func makeUTF16BE(s string) []byte {
	runes := []rune(s)
	b := make([]byte, (len(runes)+1)*2)
	for i, r := range runes {
		b[i*2] = byte(r >> 8) //nolint:gosec // G115: intentional rune→byte truncation for UTF-16 BE high byte
		b[i*2+1] = byte(r)    //nolint:gosec // G115: intentional rune→byte truncation for UTF-16 BE low byte
	}
	return b
}

// exifWithExifIFDEntry returns an *EXIF whose ExifIFD contains a single entry
// of the given type at the given tag. The EXIF stream byte order is set via
// the order parameter.
func exifWithExifIFDEntry(tag TagID, typ DataType, value []byte, order binary.ByteOrder) *EXIF { //nolint:unparam // tag is intentionally variable for test-helper reuse
	return &EXIF{
		ByteOrder: order,
		IFD0:      &IFD{},
		ExifIFD: &IFD{Entries: []IFDEntry{
			{
				Tag:       tag,
				Type:      typ,
				Count:     uint32(len(value)), //nolint:gosec // G115: test helper, intentional type cast
				Value:     value,
				bigEndian: orderIsBig(order),
			},
		}},
	}
}

// exifWithXPTag returns an *EXIF whose ExifIFD contains a TypeByte XP* entry.
func exifWithXPTag(tag TagID, value []byte) *EXIF {
	return &EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &IFD{},
		ExifIFD: &IFD{Entries: []IFDEntry{
			{
				Tag:       tag,
				Type:      TypeByte,
				Count:     uint32(len(value)), //nolint:gosec // G115: test helper, intentional type cast
				Value:     value,
				bigEndian: false,
			},
		}},
	}
}

// ---------------------------------------------------------------------------
// #27 — UserComment (0x9286) tests
// ---------------------------------------------------------------------------

func TestUserComment_ASCIIPrefix(t *testing.T) {
	t.Parallel()
	// ASCII prefix + plain ASCII text, NUL-terminated.
	// EXIF 2.32 §4.6.5: "ASCII\x00\x00\x00" prefix identifies US-ASCII text.
	text := []byte("Sunset over the Pacific\x00")
	val := makeUserComment(prefixASCII, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "Sunset over the Pacific" {
		t.Errorf("UserComment ASCII = %q, want %q", got, "Sunset over the Pacific")
	}
}

func TestUserComment_ASCIIPrefixUTF8Content(t *testing.T) {
	t.Parallel()
	// Windows writes ASCII prefix + UTF-8 multi-byte content.
	// Our decoder accepts this because UTF-8 is a superset of ASCII.
	text := []byte("Ângulo de 45°\x00")
	val := makeUserComment(prefixASCII, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "Ângulo de 45°" {
		t.Errorf("UserComment ASCII+UTF8 = %q, want %q", got, "Ângulo de 45°")
	}
}

func TestUserComment_UnicodePrefixLE(t *testing.T) {
	t.Parallel()
	// UNICODE prefix + UTF-16 LE text. Stream byte order = LE.
	// EXIF 2.32 §4.6.5: "UNICODE\x00" prefix; byte order follows the stream.
	text := makeUTF16LE("Hello Unicode")
	val := makeUserComment(prefixUnicode, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "Hello Unicode" {
		t.Errorf("UserComment UNICODE LE = %q, want %q", got, "Hello Unicode")
	}
}

func TestUserComment_UnicodePrefixBE(t *testing.T) {
	t.Parallel()
	// UNICODE prefix + UTF-16 BE text. Stream byte order = BE.
	text := makeUTF16BE("Bonjour le monde")
	val := makeUserComment(prefixUnicode, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.BigEndian)
	if got := e.UserComment(); got != "Bonjour le monde" {
		t.Errorf("UserComment UNICODE BE = %q, want %q", got, "Bonjour le monde")
	}
}

func TestUserComment_UnicodePrefixMultilingual(t *testing.T) {
	t.Parallel()
	// UNICODE prefix + UTF-16 LE with non-ASCII runes (Japanese).
	text := makeUTF16LE("写真のコメント")
	val := makeUserComment(prefixUnicode, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "写真のコメント" {
		t.Errorf("UserComment UNICODE multilingual = %q, want %q", got, "写真のコメント")
	}
}

func TestUserComment_UndefinedPrefix(t *testing.T) {
	t.Parallel()
	// All-zero charset prefix → "Undefined" per EXIF §4.6.5 Note.
	// Value treated as raw bytes (UTF-8).
	text := []byte("raw bytes comment\x00")
	var zeroPfx [8]byte // all-zero
	val := makeUserComment(zeroPfx, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "raw bytes comment" {
		t.Errorf("UserComment Undefined = %q, want %q", got, "raw bytes comment")
	}
}

func TestUserComment_JISPrefix(t *testing.T) {
	t.Parallel()
	// JIS prefix + ASCII-range bytes (best-effort pass-through).
	// Spec: EXIF §4.6.5 identifies JIS X208-1990 but provides no conversion.
	text := []byte("JIS comment\x00")
	val := makeUserComment(prefixJIS, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	// For ASCII-range content, JIS == ASCII, so we get back the original text.
	if got := e.UserComment(); got != "JIS comment" {
		t.Errorf("UserComment JIS = %q, want %q", got, "JIS comment")
	}
}

func TestUserComment_TrailingNULStripped(t *testing.T) {
	t.Parallel()
	// Multiple trailing NULs must all be stripped.
	text := []byte("padded\x00\x00\x00\x00")
	val := makeUserComment(prefixASCII, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "padded" {
		t.Errorf("UserComment trailing NULs = %q, want %q", got, "padded")
	}
}

func TestUserComment_TooShort(t *testing.T) {
	t.Parallel()
	// Payload shorter than 8 bytes → empty string (no valid prefix).
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, []byte("ABC"), binary.LittleEndian)
	if got := e.UserComment(); got != "" {
		t.Errorf("UserComment too-short = %q, want empty", got)
	}
}

func TestUserComment_ExactlyPrefixOnly(t *testing.T) {
	t.Parallel()
	// Exactly 8 bytes (prefix only, no text) → empty string.
	val := make([]byte, userCommentPrefixLen) // all-zero
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	if got := e.UserComment(); got != "" {
		t.Errorf("UserComment prefix-only = %q, want empty", got)
	}
}

func TestUserComment_MissingTag(t *testing.T) {
	t.Parallel()
	// ExifIFD exists but has no UserComment tag.
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: &IFD{}}
	if got := e.UserComment(); got != "" {
		t.Errorf("UserComment missing tag = %q, want empty", got)
	}
}

func TestUserComment_NilReceiver(t *testing.T) {
	t.Parallel()
	var e *EXIF
	if got := e.UserComment(); got != "" {
		t.Errorf("nil.UserComment() = %q, want empty", got)
	}
}

func TestUserComment_NilExifIFD(t *testing.T) {
	t.Parallel()
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: nil}
	if got := e.UserComment(); got != "" {
		t.Errorf("UserComment nil ExifIFD = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// #28 — Windows XP* tags (0x9C9B–0x9C9F) tests
// ---------------------------------------------------------------------------

func TestXPTitle_Basic(t *testing.T) {
	t.Parallel()
	// Windows XP* tags store null-terminated UTF-16 LE in TypeByte.
	// Microsoft EXIF Extension: tag 0x9C9B.
	e := exifWithXPTag(TagXPTitle, makeUTF16LE("My Holiday Photos"))
	if got := e.XPTitle(); got != "My Holiday Photos" {
		t.Errorf("XPTitle = %q, want %q", got, "My Holiday Photos")
	}
}

func TestXPComment_Basic(t *testing.T) {
	t.Parallel()
	e := exifWithXPTag(TagXPComment, makeUTF16LE("A quick comment"))
	if got := e.XPComment(); got != "A quick comment" {
		t.Errorf("XPComment = %q, want %q", got, "A quick comment")
	}
}

func TestXPAuthor_Basic(t *testing.T) {
	t.Parallel()
	e := exifWithXPTag(TagXPAuthor, makeUTF16LE("Jane Doe"))
	if got := e.XPAuthor(); got != "Jane Doe" {
		t.Errorf("XPAuthor = %q, want %q", got, "Jane Doe")
	}
}

func TestXPKeywords_Basic(t *testing.T) {
	t.Parallel()
	e := exifWithXPTag(TagXPKeywords, makeUTF16LE("nature;landscape;sunset"))
	if got := e.XPKeywords(); got != "nature;landscape;sunset" {
		t.Errorf("XPKeywords = %q, want %q", got, "nature;landscape;sunset")
	}
}

func TestXPSubject_Basic(t *testing.T) {
	t.Parallel()
	e := exifWithXPTag(TagXPSubject, makeUTF16LE("Travel"))
	if got := e.XPSubject(); got != "Travel" {
		t.Errorf("XPSubject = %q, want %q", got, "Travel")
	}
}

func TestXPTitle_Multilingual(t *testing.T) {
	t.Parallel()
	// Non-ASCII: UTF-16 LE with characters outside the ASCII range.
	e := exifWithXPTag(TagXPTitle, makeUTF16LE("Ünterwegs in Österreich"))
	if got := e.XPTitle(); got != "Ünterwegs in Österreich" {
		t.Errorf("XPTitle multilingual = %q, want %q", got, "Ünterwegs in Österreich")
	}
}

func TestXPTitle_Japanese(t *testing.T) {
	t.Parallel()
	// CJK characters encoded as UTF-16 LE.
	e := exifWithXPTag(TagXPTitle, makeUTF16LE("東京の写真"))
	if got := e.XPTitle(); got != "東京の写真" {
		t.Errorf("XPTitle Japanese = %q, want %q", got, "東京の写真")
	}
}

func TestXPTitle_NullTerminatorStripped(t *testing.T) {
	t.Parallel()
	// decodeUTF16 stops at the first null code unit; extra padding ignored.
	raw := makeUTF16LE("Title")
	// Append extra null pairs to simulate Windows padding.
	raw = append(raw, 0x00, 0x00, 0x00, 0x00)
	e := exifWithXPTag(TagXPTitle, raw)
	if got := e.XPTitle(); got != "Title" {
		t.Errorf("XPTitle with extra nulls = %q, want %q", got, "Title")
	}
}

func TestXPTitle_Empty(t *testing.T) {
	t.Parallel()
	// A two-byte null terminator only → empty string.
	e := exifWithXPTag(TagXPTitle, []byte{0x00, 0x00})
	if got := e.XPTitle(); got != "" {
		t.Errorf("XPTitle empty = %q, want empty", got)
	}
}

func TestXPTitle_MissingTag(t *testing.T) {
	t.Parallel()
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: &IFD{}}
	if got := e.XPTitle(); got != "" {
		t.Errorf("XPTitle missing tag = %q, want empty", got)
	}
}

func TestXPTitle_NilReceiver(t *testing.T) {
	t.Parallel()
	var e *EXIF
	if got := e.XPTitle(); got != "" {
		t.Errorf("nil.XPTitle() = %q, want empty", got)
	}
	if got := e.XPComment(); got != "" {
		t.Errorf("nil.XPComment() = %q, want empty", got)
	}
	if got := e.XPAuthor(); got != "" {
		t.Errorf("nil.XPAuthor() = %q, want empty", got)
	}
	if got := e.XPKeywords(); got != "" {
		t.Errorf("nil.XPKeywords() = %q, want empty", got)
	}
	if got := e.XPSubject(); got != "" {
		t.Errorf("nil.XPSubject() = %q, want empty", got)
	}
}

func TestXPTitle_NilExifIFD(t *testing.T) {
	t.Parallel()
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: nil}
	if got := e.XPTitle(); got != "" {
		t.Errorf("XPTitle nil ExifIFD = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// decodeUserComment unit tests (internal helper)
// ---------------------------------------------------------------------------

func TestDecodeUserComment_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		prefix    [8]byte
		text      []byte
		bigEndian bool
		want      string
	}{
		{
			name:   "ASCII hello",
			prefix: prefixASCII,
			text:   []byte("Hello\x00"),
			want:   "Hello",
		},
		{
			name:   "ASCII with UTF-8 content (Windows)",
			prefix: prefixASCII,
			text:   []byte("caf\xc3\xa9\x00"), // "café" in UTF-8
			want:   "café",
		},
		{
			name:      "UNICODE LE",
			prefix:    prefixUnicode,
			text:      makeUTF16LE("World"),
			bigEndian: false,
			want:      "World",
		},
		{
			name:      "UNICODE BE",
			prefix:    prefixUnicode,
			text:      makeUTF16BE("World"),
			bigEndian: true,
			want:      "World",
		},
		{
			name:   "JIS best-effort ASCII range",
			prefix: prefixJIS,
			text:   []byte("JIS text\x00"),
			want:   "JIS text",
		},
		{
			name:   "Undefined all-zero prefix",
			prefix: [8]byte{},
			text:   []byte("undefined\x00"),
			want:   "undefined",
		},
		{
			name:   "empty text after prefix",
			prefix: prefixASCII,
			text:   []byte{},
			want:   "",
		},
		{
			name:   "only NULs after ASCII prefix",
			prefix: prefixASCII,
			text:   []byte{0x00, 0x00, 0x00},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			val := makeUserComment(tc.prefix, tc.text)
			got := decodeUserComment(val, tc.bigEndian)
			if got != tc.want {
				t.Errorf("decodeUserComment() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// decodeUTF16LE unit tests (internal helper)
// ---------------------------------------------------------------------------

func TestDecodeUTF16LE_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty slice", []byte{}, ""},
		{"single byte (odd)", []byte{0x41}, ""},
		{"null only", []byte{0x00, 0x00}, ""},
		{"ascii A", []byte{0x41, 0x00, 0x00, 0x00}, "A"},
		{"hello", makeUTF16LE("hello"), "hello"},
		{"umlauts", makeUTF16LE("Ärger"), "Ärger"},
		{"no trailing null", []byte{0x48, 0x00, 0x69, 0x00}, "Hi"}, // no null term
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decodeUTF16LE(tc.in)
			if got != tc.want {
				t.Errorf("decodeUTF16LE(%x) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TagName coverage for new tags
// ---------------------------------------------------------------------------

func TestTagNameNewTags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tag  TagID
		want string
	}{
		{TagUserComment, "UserComment"},
		{TagXPTitle, "XPTitle"},
		{TagXPComment, "XPComment"},
		{TagXPAuthor, "XPAuthor"},
		{TagXPKeywords, "XPKeywords"},
		{TagXPSubject, "XPSubject"},
	}
	for _, tc := range cases {
		got := TagName(tc.tag)
		if got != tc.want {
			t.Errorf("TagName(0x%04X) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmark: UserComment decode (hot path — called once per image parse)
// ---------------------------------------------------------------------------

func BenchmarkUserComment_ASCII(b *testing.B) {
	text := []byte("A typical user comment written in plain ASCII text.\x00")
	val := makeUserComment(prefixASCII, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = e.UserComment()
	}
}

func BenchmarkUserComment_Unicode(b *testing.B) {
	text := makeUTF16LE("A typical user comment in UTF-16 LE.")
	val := makeUserComment(prefixUnicode, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, binary.LittleEndian)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = e.UserComment()
	}
}

func BenchmarkXPTitle(b *testing.B) {
	e := exifWithXPTag(TagXPTitle, makeUTF16LE("My Holiday Album 2024"))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = e.XPTitle()
	}
}
