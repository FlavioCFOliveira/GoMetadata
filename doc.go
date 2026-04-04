// Package gometadata provides a unified API for reading and writing
// EXIF, IPTC, and XMP metadata from any image format.
//
// Format detection is performed automatically by inspecting magic bytes;
// file extensions are never used. Supported containers: JPEG, TIFF, PNG,
// HEIF/HEIC, WebP, and RAW variants (CR2, CR3, NEF, ARW, DNG, ORF, RW2).
//
// Basic usage:
//
//	m, err := gometadata.ReadFile("photo.jpg")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println(m.CameraModel())
//	lat, lon, ok := m.GPS()
package gometadata
