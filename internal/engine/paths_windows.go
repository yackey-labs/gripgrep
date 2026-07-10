package engine

import "path/filepath"

// normalizeSeparators rewrites user-supplied PATH arguments to forward
// slashes. gripgrep normalizes all paths to '/' internally (see
// glob/token.go's isSeparator and walk/worker.go's joinPath): the walker
// builds every discovered child path with '/', so a root typed as
// `src\sub` would otherwise leak literal backslashes into the prefix of
// every reported path — breaking -g/--glob matching against those
// prefixes and printing mixed-separator output. Windows file APIs accept
// '/' natively, so converting up front costs nothing. rg does the
// equivalent '\'→'/' normalization when matching globs on Windows.
func normalizeSeparators(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}
