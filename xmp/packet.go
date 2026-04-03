package xmp

import "bytes"

// packetHeader is the opening processing instruction of an XMP packet (XMP §7.1).
var packetHeader = []byte("<?xpacket begin=")

// Scan locates the XMP packet boundaries within b and returns the slice that
// spans from the opening <?xpacket begin=…?> to the closing <?xpacket end=…?>.
// Returns nil if no packet is found.
func Scan(b []byte) []byte {
	start := bytes.Index(b, packetHeader)
	if start < 0 {
		return nil
	}
	// Find the closing processing instruction.
	end := bytes.Index(b[start:], []byte("?>"))
	if end < 0 {
		return nil
	}
	// The full packet ends after the closing ?>.
	tail := bytes.Index(b[start+end+2:], []byte("?>"))
	if tail < 0 {
		return nil
	}
	fullEnd := start + end + 2 + tail + 2
	if fullEnd > len(b) {
		return nil
	}
	return b[start:fullEnd]
}
