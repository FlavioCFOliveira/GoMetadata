package rw2

import "errors"

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Extract or Inject call. Reads via
// io.ReadAll are wrapped with io.LimitReader(r, maxFileSize+1); if the reader
// delivers more bytes than this limit the operation is aborted with
// ErrFileTooLarge before any allocation proportional to file size is retained.
//
// Real-world Panasonic RW2 files are well under 100 MiB; 256 MiB gives ample
// headroom for future camera improvements while bounding worst-case heap
// allocation to a predictable, safe value.
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the OOM-guard path without allocating 256 MiB of memory.
//
// #140 fix: cap uncapped io.ReadAll calls to prevent OOM on oversized or
// infinite streaming readers.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned when the input exceeds maxFileSize. This prevents
// a streaming or adversarially large reader from causing unbounded heap
// allocation. Callers can detect this specific condition with errors.Is.
var ErrFileTooLarge = errors.New("rw2: input exceeds maximum file size (256 MiB)")

// ErrInvalidMagic is returned when the input does not begin with the Panasonic RW2 magic bytes.
var ErrInvalidMagic = errors.New("rw2: invalid magic bytes")

// ErrOutputTooShort is returned when the reconstructed RW2 output is shorter than expected.
var ErrOutputTooShort = errors.New("rw2: output too short")
