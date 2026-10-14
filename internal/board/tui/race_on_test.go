//go:build race

package tui

// raceSlowdown widens timing budgets: the race detector makes code several times slower.
const raceSlowdown = 8
