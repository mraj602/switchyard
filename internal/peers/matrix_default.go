//go:build !peerstest

package peers

// DefaultMatrix is the matrix a release build uses: the accepted signed document in the state
// folder (LocalMatrix), or none, so every send is refused with "matrix not configured".
func DefaultMatrix(state string) Matrix { return LocalMatrix(state) }
