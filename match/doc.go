// Package match compiles search patterns (regex or fixed strings) into a
// Matcher: rg's "grep-regex" equivalent. A Matcher extracts required
// literals from the pattern (prefix/inner/suffix), runs a SIMD-backed
// literal prefilter over whole buffers via bytes.Index/bytes.IndexByte,
// and falls through to a real regex engine only to confirm candidate
// hits on a single line. Plain literal patterns skip the regex engine
// entirely.
//
// Every hot-path method takes []byte, never string, and is designed to
// run allocation-free in steady state once a Matcher is constructed.
package match
