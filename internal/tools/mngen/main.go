// Command mngen generates exif/makernote_tags_gen.go, the MakerNote tag-name
// tables, from the MakerNote directory classes of metadata-extractor
// (https://github.com/drewnoakes/metadata-extractor, Apache-2.0).
//
// Only tag IDs and tag names are read: the "public static final int TAG_*"
// constants declared directly in the directory class and the
// _tagNameMap.put(TAG_*, "Name") calls of its static initialiser. Constants
// of nested classes (for example Canon's CameraSettings, whose IDs are
// synthetic array offsets and not IDs of the MakerNote IFD), value
// descriptors and lookup tables are never read.
//
// Directories whose source header attributes the tag reference to ExifTool
// are not read (see excluded): ExifTool is GPL or Artistic licensed and is
// used by this project only as a test oracle.
//
// Usage (from the exif directory, see makernote_names.go):
//
//	go run ../internal/tools/mngen -src <metadata-extractor checkout> -out makernote_tags_gen.go
//
// The checkout must be at the commit recorded in THIRD_PARTY_NOTICES.md; the
// generator reads the commit from the checkout with git and writes it into the
// generated file. The output is deterministic.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// makernotesDir is the location of the directory classes inside a
// metadata-extractor checkout.
const makernotesDir = "Source/com/drew/metadata/exif/makernotes"

// table is one generated table: the Java class it is read from and the Go
// identifier suffix of the generated variable.
type table struct {
	// ident is the suffix of the generated variable makerNoteTags<Ident>.
	ident string
	// class is the metadata-extractor directory class (file name without .java).
	class string
}

// tables lists every directory class that is read, in output order.
//
//nolint:gochecknoglobals // read-only generator configuration
var tables = []table{
	{"Canon", "CanonMakernoteDirectory"},
	{"CasioType1", "CasioType1MakernoteDirectory"},
	{"CasioType2", "CasioType2MakernoteDirectory"},
	{"Fujifilm", "FujifilmMakernoteDirectory"},
	{"Kyocera", "KyoceraMakernoteDirectory"},
	{"Leica", "LeicaMakernoteDirectory"},
	{"NikonType1", "NikonType1MakernoteDirectory"},
	{"NikonType2", "NikonType2MakernoteDirectory"},
	{"Olympus", "OlympusMakernoteDirectory"},
	{"Panasonic", "PanasonicMakernoteDirectory"},
	{"Pentax", "PentaxMakernoteDirectory"},
	{"Ricoh", "RicohMakernoteDirectory"},
	{"Sanyo", "SanyoMakernoteDirectory"},
	{"Sigma", "SigmaMakernoteDirectory"},
	{"SonyType1", "SonyType1MakernoteDirectory"},
	{"SonyType6", "SonyType6MakernoteDirectory"},
}

// excluded lists the directory classes that are deliberately not read, with
// the reason. The generator refuses to read a class that is in this list.
//
//nolint:gochecknoglobals // read-only generator configuration
var excluded = map[string]string{
	"AppleMakernoteDirectory":        "header: \"Using information from\" the ExifTool Apple tag documentation",
	"DjiMakernoteDirectory":          "header: \"Using information from\" the ExifTool DJI tag documentation",
	"LeicaType5MakernoteDirectory":   "header: \"Tag reference from\" the ExifTool Panasonic tag documentation",
	"SamsungType2MakernoteDirectory": "header: \"Tag reference from\" the ExifTool Samsung tag documentation",
}

// tag is one tag ID and its name.
type tag struct {
	id   uint16
	name string
}

var (
	// constRE matches a tag constant declared directly in the class body
	// (one indent level). Nested classes indent their constants further, so
	// they never match. Values that are expressions (OFFSET + 0x01) do not
	// match either.
	constRE = regexp.MustCompile(`(?m)^(?: {4}|\t)public (?:static )?final (?:static )?int ((?:TAG_|Tag)\w+)\s*=\s*(0[xX][0-9a-fA-F]+|\d+)\s*;`)
	// nameRE matches _tagNameMap.put(TAG_X, "Name").
	nameRE = regexp.MustCompile(`_tagNameMap\.put\(\s*((?:TAG_|Tag)\w+)\s*,\s*"([^"]*)"\s*\)`)
)

// errNoTags is returned for a class that yields no tag.
var errNoTags = errors.New("no tag constants found")

// parseClass extracts the (ID, name) pairs of a directory class source. A
// constant that has no _tagNameMap entry has no name and is reported in
// unnamed, not generated. When two constants have the same ID, the first one
// wins and the duplicate is reported in dups. The result is sorted by ID.
func parseClass(src string) (tags []tag, dups, unnamed []string, err error) {
	names := map[string]string{}
	for _, m := range nameRE.FindAllStringSubmatch(src, -1) {
		if _, ok := names[m[1]]; !ok {
			names[m[1]] = m[2]
		}
	}
	seen := map[uint16]string{}
	for _, m := range constRE.FindAllStringSubmatch(src, -1) {
		v, perr := strconv.ParseUint(m[2], 0, 32)
		if perr != nil || v > 0xFFFF {
			return nil, nil, nil, fmt.Errorf("%s = %s: %w", m[1], m[2], errBadTagID)
		}
		id := uint16(v)
		if prev, ok := seen[id]; ok {
			dups = append(dups, fmt.Sprintf("0x%04X: %s duplicates %s", id, m[1], prev))
			continue
		}
		seen[id] = m[1]
		name, ok := names[m[1]]
		if !ok {
			// A constant without a _tagNameMap entry (an encrypted block that
			// the reference decodes elsewhere) has no name to transcribe.
			unnamed = append(unnamed, m[1])
			continue
		}
		tags = append(tags, tag{id: id, name: camel(name)})
	}
	if len(tags) == 0 {
		return nil, nil, nil, errNoTags
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].id < tags[j].id })
	return tags, dups, unnamed, nil
}

// camel converts a metadata-extractor tag name ("Lens Model", "WB RGB
// Levels", "TAG_LENS_MODEL") to the CamelCase style of exif.TagName: the text
// is split at every character that is not a letter or a digit, the first
// letter of each word is upper-cased, and the words are joined.
func camel(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			up = true
			continue
		}
		if up {
			r = unicode.ToUpper(r)
			up = false
		}
		_, _ = b.WriteRune(r)
	}
	return b.String()
}

func main() {
	src := flag.String("src", "", "metadata-extractor checkout")
	out := flag.String("out", "makernote_tags_gen.go", "output file")
	flag.Parse()
	if err := run(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "mngen:", err)
		os.Exit(1)
	}
}

// Errors of the generator.
var (
	errNoSource      = errors.New("-src is required")
	errExcludedClass = errors.New("class is excluded")
	errBadTagID      = errors.New("constant is not a 16-bit tag ID")
)

func run(src, out string) error {
	if src == "" {
		return errNoSource
	}
	commit, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output() //nolint:gosec // G204: fixed command, path from the developer's flag
	if err != nil {
		return fmt.Errorf("read the checkout commit: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/tools/mngen from metadata-extractor; DO NOT EDIT.\n\n")
	buf.WriteString("package exif\n\n")
	buf.WriteString("// Adapted from metadata-extractor Source/com/drew/metadata/exif/makernotes/*MakernoteDirectory.java:TAG_*,_tagNameMap, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0\n//\n")
	fmt.Fprintf(&buf, "// Source commit: %s\n", strings.TrimSpace(string(commit)))
	buf.WriteString("// See THIRD_PARTY_NOTICES.md. Only tag IDs and tag names are taken from the\n")
	buf.WriteString("// directory classes; no value descriptor or lookup table is used. The names are\n")
	buf.WriteString("// converted to the CamelCase style of TagName. Changes: the Java constants and\n")
	buf.WriteString("// _tagNameMap entries are transcribed to sorted Go arrays.\n\n")
	for _, t := range tables {
		if werr := writeTable(&buf, src, t); werr != nil {
			return werr
		}
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format the generated source: %w", err)
	}
	return os.WriteFile(out, formatted, 0o644) //nolint:gosec // G306: a source file, world-readable by design
}

// writeTable reads one directory class of the checkout src and appends its
// table to buf. Skipped and duplicate constants are reported on stderr.
func writeTable(buf *bytes.Buffer, src string, t table) error {
	if why, bad := excluded[t.class]; bad {
		return fmt.Errorf("%s (%s): %w", t.class, why, errExcludedClass)
	}
	data, err := os.ReadFile(filepath.Join(src, makernotesDir, t.class+".java"))
	if err != nil {
		return fmt.Errorf("read %s: %w", t.class, err)
	}
	tags, dups, unnamed, err := parseClass(string(data))
	if err != nil {
		return fmt.Errorf("%s: %w", t.class, err)
	}
	for _, d := range dups {
		fmt.Fprintf(os.Stderr, "mngen: %s: %s\n", t.class, d)
	}
	for _, u := range unnamed {
		fmt.Fprintf(os.Stderr, "mngen: %s: %s has no name entry, skipped\n", t.class, u)
	}
	fmt.Fprintf(buf, "// makerNoteTags%s holds the %d tags of %s, sorted by ID.\n", t.ident, len(tags), t.class)
	fmt.Fprintf(buf, "var makerNoteTags%s = [...]makerNoteTag{ //nolint:gochecknoglobals // read-only generated table\n", t.ident)
	for _, g := range tags {
		fmt.Fprintf(buf, "\t{0x%04X, %q},\n", g.id, g.name)
	}
	buf.WriteString("}\n\n")
	return nil
}
