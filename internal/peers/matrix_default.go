//go:build !peerstest

package peers

// DefaultMatrix is the matrix a release build uses until the keys and matrix package is
// wired in: none, so every send is refused with "matrix not configured".
func DefaultMatrix(state string) Matrix { return NoMatrix{} }
