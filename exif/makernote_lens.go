package exif

// Adapted from metadata-extractor Source/com/drew/metadata/TagDescriptor.java:getLensSpecificationDescription, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0
// Adapted from metadata-extractor Source/com/drew/lang/Rational.java:toSimpleString, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0

import (
	"math"
	"strconv"
)

// Lens tags of the MakerNote IFDs. The IDs are those of metadata-extractor
// (CanonMakernoteDirectory.TAG_LENS_MODEL, NikonType2MakernoteDirectory.TAG_LENS).
const (
	canonTagLensModel TagID = 0x0095 // ASCII: the lens model, e.g. "EF-S18-55mm f/3.5-5.6 IS II"
	nikonTagLens      TagID = 0x0084 // RATIONAL[4]: min and max focal length, min and max f-number
)

// MakerNoteLens describes the lens recorded in the MakerNote, or returns an
// empty string when the MakerNote holds no lens description this library can
// read. It is the fallback of Metadata.LensModel for files whose Exif IFD has
// no LensModel tag (0xA434).
//
// Two descriptions are read:
//
//   - Canon: the LensModel tag (0x0095), the lens model as the camera wrote it
//     ("EF-S18-55mm f/3.5-5.6 IS II"). Cameras that predate the tag, most
//     PowerShot models, have none.
//   - Nikon: the Lens tag (0x0084), the lens specification, formatted as
//     "18-55mm f/3.5-5.6" (focal range, aperture range); a prime lens reads
//     "50mm f/1.8" and a constant f-number "24-70mm f/4". It is the
//     specification of the lens, not its marketing name.
//
// The numeric lens identifiers of Canon (LensType), Nikon (LensIDNumber) and
// Sony (LensID) are not translated to names: the lookup tables of
// metadata-extractor for them derive from ExifTool, or do not exist, and this
// library does not copy ExifTool.
//
// It returns an empty string when e is nil, when the MakerNote was not parsed
// (the SkipMakerNote option) or when it has no IFD.
func (e *EXIF) MakerNoteLens() string {
	if e == nil || e.MakerNoteIFD == nil {
		return ""
	}
	ctx, ok := e.MakerNoteContext()
	if !ok {
		return ""
	}
	switch ctx { //nolint:exhaustive // only Canon and Nikon notes carry a lens description
	case ContextMakerNoteCanon:
		return e.MakerNoteIFD.Get(canonTagLensModel).blankTrimmedString()
	case ContextMakerNoteNikon:
		return nikonLensSpec(e.MakerNoteIFD.Get(nikonTagLens))
	}
	return ""
}

// nikonLensSpec formats the Nikon Lens tag. It follows
// TagDescriptor.getLensSpecificationDescription of metadata-extractor: four
// RATIONAL values [min focal, max focal, min f-number, max f-number]; the
// description is empty when the count is not four or when both the minimum
// focal length and the minimum f-number are zero. It returns an empty string
// when a denominator is zero (a value that cannot be a lens specification).
func nikonLensSpec(ent *IFDEntry) string {
	v, ok := nikonLensRationals(ent)
	if !ok {
		return ""
	}
	// 48 bytes hold every description of realistic values; a larger result
	// grows the slice once.
	var buf [48]byte
	b := appendSimpleRational(buf[:0], v[0])
	if !ratEqual(v[0], v[1]) {
		b = append(b, '-')
		b = appendSimpleRational(b, v[1])
	}
	b = append(b, "mm"...)
	if v[2][0] != 0 {
		b = append(b, " f/"...)
		b = appendOneDecimal(b, v[2])
		if !ratEqual(v[2], v[3]) {
			b = append(b, '-')
			b = appendOneDecimal(b, v[3])
		}
	}
	return string(b)
}

// nikonLensRationals reads the four rationals of a Nikon Lens entry. ok is
// false when the entry is not four RATIONAL values with non-zero denominators,
// or when both the minimum focal length and the minimum f-number are zero (an
// unknown lens).
func nikonLensRationals(ent *IFDEntry) (v [4][2]uint32, ok bool) {
	if ent == nil || ent.Type != TypeRational || ent.Count != 4 {
		return v, false
	}
	for i := range v {
		v[i] = ent.Rational(i)
		if v[i][1] == 0 {
			return v, false
		}
	}
	return v, v[0][0] != 0 || v[2][0] != 0
}

// ratEqual reports whether two rationals with non-zero denominators are
// mathematically equal (Rational.equals in metadata-extractor).
func ratEqual(a, b [2]uint32) bool {
	return uint64(a[0])*uint64(b[1]) == uint64(b[0])*uint64(a[1])
}

// appendSimpleRational appends r (denominator not zero) as
// Rational.toSimpleString(true) does: an integer when the value is one; the
// decimal value when it is shorter than five characters; the reduced fraction
// otherwise.
func appendSimpleRational(b []byte, r [2]uint32) []byte {
	n, d := uint64(r[0]), uint64(r[1])
	if n%d == 0 {
		return strconv.AppendUint(b, n/d, 10)
	}
	g := gcd(n, d)
	n, d = n/g, d/g
	var tmp [24]byte
	dec := strconv.AppendFloat(tmp[:0], float64(n)/float64(d), 'f', -1, 64)
	if len(dec) < 5 {
		return append(b, dec...)
	}
	b = strconv.AppendUint(b, n, 10)
	b = append(b, '/')
	return strconv.AppendUint(b, d, 10)
}

// appendOneDecimal appends the f-number r (denominator not zero) rounded half
// up to one decimal place, as the "0.0" DecimalFormat of metadata-extractor
// does, with a zero decimal dropped ("4", not "4.0"): the form ExifTool prints,
// which keeps the description equal to its Lens tag.
func appendOneDecimal(b []byte, r [2]uint32) []byte {
	x := float64(r[0]) / float64(r[1])
	n := len(b)
	b = strconv.AppendFloat(b, math.Floor(x*10+0.5)/10, 'f', 1, 64)
	if len(b)-n > 2 && b[len(b)-2] == '.' && b[len(b)-1] == '0' {
		b = b[:len(b)-2]
	}
	return b
}

// gcd returns the greatest common divisor of a and b (b not zero).
func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
