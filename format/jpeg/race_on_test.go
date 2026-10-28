//go:build race

package jpeg

// raceEnabled reports whether the race detector is on: it makes sync.Pool drop
// puts at random, so heap-growth bounds are not deterministic (task #419).
const raceEnabled = true
