---
name: Example function output trailing spaces
description: Go 1.26 does not strip trailing whitespace from example test output lines; fmt.Println with empty string args produces trailing spaces that fail // Output: comparisons
type: feedback
---

`fmt.Println("Label:", value)` produces `"Label: \n"` when `value == ""` (one trailing space from the arg separator). Go 1.26's `go test` does NOT normalize trailing whitespace in example output comparisons, so the test fails.

**Why:** `fmt.Println` always inserts a space between arguments, even when the second argument is an empty string. The `// Output:` comment `// Label:` (no trailing space) does not match `"Label: "`.

**How to apply:** In example functions where a field value might be empty, either:
1. Use `if val != ""; fmt.Println("Label:", val) else fmt.Println("Label:")` pattern
2. Use `fmt.Printf("Label: %s\n", val)` only when the format string does not have trailing spaces before `%s`
3. Or use `if val := m.Field(); val != "" { fmt.Println("Label:", val) } else { fmt.Println("Label:") }`

The third form (if-else in example body) is cleanest for aligned output with potentially empty fields.
