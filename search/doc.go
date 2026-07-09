// Package search drives a match.Matcher over one file's bytes and reports
// results to a Sink: rg's "grep-searcher" equivalent. It owns the rolling
// 64KB read buffer, the fast whole-buffer candidate path (for patterns
// that can't match across a line terminator) and the slower per-line
// path, NUL-based binary detection, lazy line-number counting, and
// leading/trailing context tracking.
//
// Sink implementations (see package printer) receive []byte fields that
// are only valid for the duration of the call that delivers them — see
// the Match and Ctx doc comments for the exact contract.
package search
