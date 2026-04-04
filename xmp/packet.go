package xmp

import "bytes"

// packetHeader is the opening processing instruction of an XMP packet (XMP §7.1).
var packetHeader = []byte("<?xpacket begin=") //nolint:gochecknoglobals // package-level constant bytes

// xpacketClose is the opening of the mandatory closing processing instruction
// of an XMP packet (XMP §7.1). Searching for this string (rather than any "?>")
// avoids false termination on processing instructions inside the XMP body.
var xpacketClose = []byte("<?xpacket end=") //nolint:gochecknoglobals // package-level constant bytes

// Scan locates the XMP packet boundaries within b and returns the slice that
// spans from the opening <?xpacket begin=…?> to the closing <?xpacket end=…?>.
// Returns nil if no packet is found.
func Scan(b []byte) []byte {
	start := bytes.Index(b, packetHeader)
	if start < 0 {
		return nil
	}
	// Find the end of the opening processing instruction (first ?> after start).
	openEnd := bytes.Index(b[start:], []byte("?>"))
	if openEnd < 0 {
		return nil
	}
	openEnd = start + openEnd + 2 // absolute position past the opening PI

	// Find the closing <?xpacket end=…?> specifically (XMP §7.1).
	// Searching for the exact opening of the close PI avoids being misled by
	// any ?> characters that appear inside the XMP body.
	tail := bytes.Index(b[openEnd:], xpacketClose)
	if tail < 0 {
		return nil
	}
	closeStart := openEnd + tail
	// Find the ?> that terminates the closing PI.
	closeEnd := bytes.Index(b[closeStart:], []byte("?>"))
	if closeEnd < 0 {
		return nil
	}
	fullEnd := closeStart + closeEnd + 2
	if fullEnd > len(b) {
		return nil
	}
	return b[start:fullEnd]
}
