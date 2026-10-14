//go:build !race

package hook

// raceEnabled relaxes timing budgets under the race detector.
const raceEnabled = false
