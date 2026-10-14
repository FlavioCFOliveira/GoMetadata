package gometadata

// heif_task254_test.go — task #254: HEIF files whose EXIF ExifTool reads but
// GoMetadata returned as empty. Root cause: the iloc data_reference_index
// omission of task #282. The expected values below are ExifTool 13 output
// (exiftool -n -Software -ExposureTime -ISO -GPSLatitude -GPSLongitude).

import (
	"bytes"
	"math"
	"testing"
)

func TestHEIF_Task254_NokiaEXIF(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rel          string
		expNum, expD uint32
		iso          uint
		lat, lon     float64
	}{
		{"exif-samples/HMD_Nokia_8.3_5G.heif", 1, 5000, 102, 40.78868522, -4.00514085},
		{"exif-samples/HMD_Nokia_8.3_5G_hdr.heif", 1, 125, 100, 40.6629935, -3.76374051},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(readHEIFCorpus(t, tc.rel)))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(m.RawEXIF()) == 0 {
				t.Fatal("EXIF is empty, ExifTool reads it")
			}
			if got := m.Software(); got != "00WW_2_270_SP04" {
				t.Errorf("Software = %q, want %q", got, "00WW_2_270_SP04")
			}
			// The stored RATIONAL is not reduced (e.g. 2/10000); ExifTool
			// prints the reduced value, so compare the quotients.
			if n, d, ok := m.ExposureTime(); !ok || d == 0 || uint64(n)*uint64(tc.expD) != uint64(d)*uint64(tc.expNum) {
				t.Errorf("ExposureTime = %d/%d (%v), want %d/%d", n, d, ok, tc.expNum, tc.expD)
			}
			if iso, ok := m.ISO(); !ok || iso != tc.iso {
				t.Errorf("ISO = %d (%v), want %d", iso, ok, tc.iso)
			}
			lat, lon, ok := m.GPS()
			if !ok || math.Abs(lat-tc.lat) > 1e-6 || math.Abs(lon-tc.lon) > 1e-6 {
				t.Errorf("GPS = %v,%v (%v), want %v,%v", lat, lon, ok, tc.lat, tc.lon)
			}
		})
	}
}
