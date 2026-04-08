// Command raw-inspector reads and displays all available metadata from camera
// RAW files. It supports Canon CR2/CR3, Nikon NEF, Sony ARW, Adobe DNG,
// Olympus ORF, and Panasonic RW2 — all automatically detected by magic bytes,
// with no external dependencies.
//
// This example shows how to use GoMetadata (github.com/FlavioCFOliveira/GoMetadata)
// as a Go EXIF reader for RAW files: extract camera make and model, lens model,
// shooting parameters (shutter speed, aperture, ISO, focal length), GPS
// coordinates and altitude, orientation, white balance, flash, metering mode,
// colour space, and capture date from any major RAW format with one unified API.
//
// Usage:
//
//	raw-inspector <rawfile>
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: raw-inspector <rawfile>")
		os.Exit(1)
	}

	path := os.Args[1]

	// MakerNote parsing is the costliest part of EXIF; skip it when only
	// standard tags are needed.
	m, err := gometadata.ReadFile(path, gometadata.WithoutMakerNote())
	if err != nil {
		var unsupported *gometadata.UnsupportedFormatError
		if errors.As(err, &unsupported) {
			fmt.Fprintf(os.Stderr, "raw-inspector: unsupported format (magic: %x)\n", unsupported.Magic[:])
			os.Exit(1)
		}
		log.Fatal(err)
	}

	// --- Container format ---
	fmt.Printf("%-16s %s\n", "Format:", m.Format())

	// --- Camera identification ---
	printField("Make:", m.Make())
	printField("Model:", m.CameraModel())
	printField("Lens:", m.LensModel())
	printField("Software:", m.Software())

	// --- Date / time ---
	if t, ok := m.DateTimeOriginal(); ok {
		fmt.Printf("%-16s %s  (%s)\n", "Captured:", t.Format(time.RFC3339), t.Format("Mon Jan 2 2006"))
	} else {
		fmt.Printf("%-16s %s\n", "Captured:", "(not set)")
	}

	// --- Image dimensions ---
	if w, h, ok := m.ImageSize(); ok {
		fmt.Printf("%-16s %dx%d px\n", "Size:", w, h)
	} else {
		fmt.Printf("%-16s %s\n", "Size:", "(not set)")
	}

	// --- Orientation ---
	if ori, ok := m.Orientation(); ok {
		fmt.Printf("%-16s %d (%s)\n", "Orientation:", ori, orientationDesc(ori))
	} else {
		fmt.Printf("%-16s %s\n", "Orientation:", "(not set)")
	}

	// --- Shooting parameters ---
	fmt.Println()
	fmt.Println("— Shooting parameters —")

	if num, den, ok := m.ExposureTime(); ok {
		if num == 1 {
			fmt.Printf("%-16s 1/%d s\n", "ExposureTime:", den)
		} else {
			fmt.Printf("%-16s %d/%d s\n", "ExposureTime:", num, den)
		}
	} else {
		fmt.Printf("%-16s %s\n", "ExposureTime:", "(not set)")
	}

	if fn, ok := m.FNumber(); ok {
		fmt.Printf("%-16s f/%.1f\n", "FNumber:", fn)
	} else {
		fmt.Printf("%-16s %s\n", "FNumber:", "(not set)")
	}

	if iso, ok := m.ISO(); ok {
		fmt.Printf("%-16s %d\n", "ISO:", iso)
	} else {
		fmt.Printf("%-16s %s\n", "ISO:", "(not set)")
	}

	if fl, ok := m.FocalLength(); ok {
		fmt.Printf("%-16s %.1f mm\n", "FocalLength:", fl)
	} else {
		fmt.Printf("%-16s %s\n", "FocalLength:", "(not set)")
	}

	if em, ok := m.ExposureMode(); ok {
		fmt.Printf("%-16s %d (%s)\n", "ExposureMode:", em, exposureModeDesc(em))
	} else {
		fmt.Printf("%-16s %s\n", "ExposureMode:", "(not set)")
	}

	if wb, ok := m.WhiteBalance(); ok {
		fmt.Printf("%-16s %d (%s)\n", "WhiteBalance:", wb, whiteBalanceDesc(wb))
	} else {
		fmt.Printf("%-16s %s\n", "WhiteBalance:", "(not set)")
	}

	if fl, ok := m.Flash(); ok {
		fired := "(not fired)"
		if fl&0x1 != 0 {
			fired = "(fired)"
		}
		fmt.Printf("%-16s %d %s\n", "Flash:", fl, fired)
	} else {
		fmt.Printf("%-16s %s\n", "Flash:", "(not set)")
	}

	if mm, ok := m.MeteringMode(); ok {
		fmt.Printf("%-16s %d (%s)\n", "MeteringMode:", mm, meteringModeDesc(mm))
	} else {
		fmt.Printf("%-16s %s\n", "MeteringMode:", "(not set)")
	}

	if cs, ok := m.ColorSpace(); ok {
		fmt.Printf("%-16s %d (%s)\n", "ColorSpace:", cs, colorSpaceDesc(cs))
	} else {
		fmt.Printf("%-16s %s\n", "ColorSpace:", "(not set)")
	}

	if sd, ok := m.SubjectDistance(); ok {
		fmt.Printf("%-16s %.1f m\n", "SubjectDist:", sd)
	} else {
		fmt.Printf("%-16s %s\n", "SubjectDist:", "(not set)")
	}

	// --- Location ---
	fmt.Println()
	fmt.Println("— Location —")

	if lat, lon, ok := m.GPS(); ok {
		fmt.Printf("%-16s %.6f, %.6f\n", "GPS:", lat, lon)
	} else {
		fmt.Printf("%-16s %s\n", "GPS:", "(not set)")
	}

	if alt, ok := m.Altitude(); ok {
		dir := "above"
		if alt < 0 {
			dir = "below"
		}
		fmt.Printf("%-16s %.1f m %s sea level\n", "Altitude:", alt, dir)
	} else {
		fmt.Printf("%-16s %s\n", "Altitude:", "(not set)")
	}

	// --- Descriptive ---
	fmt.Println()
	fmt.Println("— Descriptive —")

	printField("Caption:", m.Caption())
	printField("Copyright:", m.Copyright())
	printField("Creator:", m.Creator())

	if kws := m.Keywords(); len(kws) > 0 {
		for i, kw := range kws {
			if i == 0 {
				fmt.Printf("%-16s %s\n", "Keywords:", kw)
			} else {
				fmt.Printf("%-16s %s\n", "", kw)
			}
		}
	} else {
		fmt.Printf("%-16s %s\n", "Keywords:", "(not set)")
	}
}

// printField prints a label and string value, or "(not set)" when the string is empty.
func printField(label, value string) {
	if value != "" {
		fmt.Printf("%-16s %s\n", label, value)
	} else {
		fmt.Printf("%-16s %s\n", label, "(not set)")
	}
}

// orientationDesc returns a human-readable description for EXIF orientation values.
// EXIF 2.32, CIPA DC-008, Table 6: Orientation tag (0x0112).
func orientationDesc(v uint16) string {
	switch v {
	case 1:
		return "normal"
	case 3:
		return "180°"
	case 6:
		return "90° CW"
	case 8:
		return "90° CCW"
	default:
		return "other"
	}
}

// exposureModeDesc returns a description for EXIF ExposureMode values (tag 0xA402).
func exposureModeDesc(v uint16) string {
	switch v {
	case 0:
		return "auto"
	case 1:
		return "manual"
	case 2:
		return "auto-bracket"
	default:
		return "unknown"
	}
}

// whiteBalanceDesc returns a description for EXIF WhiteBalance values (tag 0xA403).
func whiteBalanceDesc(v uint16) string {
	switch v {
	case 0:
		return "auto"
	case 1:
		return "manual"
	default:
		return "unknown"
	}
}

// meteringModeDesc returns a description for EXIF MeteringMode values (tag 0x9207).
func meteringModeDesc(v uint16) string {
	switch v {
	case 0:
		return "unknown"
	case 1:
		return "average"
	case 2:
		return "center-weighted average"
	case 3:
		return "spot"
	case 4:
		return "multi-spot"
	case 5:
		return "pattern"
	case 6:
		return "partial"
	default:
		return "other"
	}
}

// colorSpaceDesc returns a description for EXIF ColorSpace values (tag 0xA001).
func colorSpaceDesc(v uint16) string {
	switch v {
	case 1:
		return "sRGB"
	case 0xFFFF:
		return "uncalibrated"
	default:
		return "unknown"
	}
}
