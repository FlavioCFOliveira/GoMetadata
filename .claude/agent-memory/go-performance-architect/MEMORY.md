# Memory Index

- [Project identity](project_identity.md) — GoMetadata; module github.com/FlavioCFOliveira/GoMetadata; 25 packages; Go 1.26
- [os.IsNotExist vs errors.Is pattern](feedback_os_error_wrapping.md) — os.IsNotExist/IsPermission do not unwrap %w errors in Go 1.26; tests must use errors.Is with sentinel values
- [Example output trailing spaces](feedback_example_output.md) — Go 1.26 does NOT strip trailing whitespace in example output; fmt.Println with empty string args fails // Output: comparisons
- [intrange + modernize nolint for binary parsers](feedback_intrange_nolint.md) — Loops using i*12 offset need //nolint:intrange,modernize; min builtin shadowed in fuzz_test.go affects test builds
- [sync.Pool buffer subslice race](feedback_pool_buffer_race.md) — Never Put a pool buffer before all reads of subslices derived from it are complete; exposed by t.Parallel() in detect.go and heif.go
