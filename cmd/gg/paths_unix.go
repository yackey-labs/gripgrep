//go:build unix

package main

// normalizeSeparators is a no-op on unix: '/' is the only separator, so
// user-supplied paths are already in the normalized form the walker and
// glob matcher use internally (see paths_windows.go for why Windows
// can't say the same).
func normalizeSeparators(paths []string) []string {
	return paths
}
