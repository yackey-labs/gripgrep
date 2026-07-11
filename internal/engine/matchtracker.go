package engine

import (
	"bytes"
	"strconv"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
)

// matchTracker wraps a search.Sink to add two things no printer sink can
// do on its own, since Standard/Count/FilesWithMatches never see
// search.Stats until their own Finish (and Sink.Finish's return value
// doesn't propagate a "matched" signal back to the caller):
//
//  1. Recording whether any file, anywhere, ever matched -- for the
//     process exit code (cmd/gg) or a caller's own bookkeeping (the root
//     facade uses the same Result.Matched).
//  2. rg's binary-file special-casing, verified against the real rg
//     14.1.1 binary (see the M2 handoff notes and task #20 for the exact
//     probes, including ../ripgrep/tests/data/sherlock-nul.txt and
//     ripgrep's own upstream searcher tests binary1-binary4):
//     - BinaryQuit + Stats.Binary, Standard mode: any matches already
//     found in earlier, NUL-free reads were already sunk into the
//     underlying sink's buffer before Finish ever ran (search's own
//     BinaryQuit discards only the single read chunk that contains
//     the NUL, not the whole file -- see linebuffer.go's fill), so
//     they're flushed normally, followed by rg's own
//     "WARNING: stopped searching binary file after match..." line
//     (verified against the real rg binary on the sherlock-nul.txt
//     fixture: real matches print, then that exact warning).
//     If Stats.Matched is false, nothing was found at all (the NUL
//     fell in the very first read), so nothing is printed -- rg is
//     silent in that case too (verified: a small file whose one-and-
//     only read contains a match immediately followed by a NUL
//     reports zero matches and prints no warning).
//     - BinaryQuit + Stats.Binary, -c/-l (standard=false): discarded
//     entirely regardless of Stats.Matched -- verified against the
//     real rg binary: `rg -c`/`rg -l` on the same sherlock-nul.txt
//     walk show nothing and exit 1, unlike the real count/path they'd
//     show for an explicitly-named (Convert-mode) binary file. This
//     drop is engine semantics, not CLI presentation: it changes
//     match counts and file lists, so the root facade's CountMatches/
//     FilesWithMatch share it too (see Run's doc).
//     - BinaryConvert + Stats.Binary + Stats.Matched, Standard mode only:
//     rg replaces the file's entire per-line output with one generic
//     `binary file matches (found "\0" byte around offset N)` line
//     instead of normal match formatting. -c/-l are unaffected --
//     rg shows their real count/path exactly as it would for a text
//     file (verified: `rg -c` on a binary file with a NUL after one
//     match still prints the true match count).
//
// Known gap: -q (Quiet) records a match the instant Sink.Matched fires,
// before Finish (and therefore before Stats.Binary is known) -- so a
// walk-discovered binary file with a match before its first NUL byte
// will incorrectly count towards -q's exit code. This combination isn't
// in gg's v1 golden matrix; flagged for follow-up rather than fixed here.
//
// Known approximation: -uuu (BinarySearchAndSuppress, resolved to
// search.BinaryConvert for every file, not just explicit ones -- see
// resolveBinaryMode) routes through the same "generic binary message"
// branch as an explicitly-named file's default Convert mode. Real rg's
// --binary instead prints the actual matching lines plus a trailing
// "WARNING: ... stopped searching prematurely" note -- a different
// output shape entirely. -uuu is untested (no golden case exercises it);
// documented here rather than fixed, since matching it exactly would
// need a third matchTracker branch with its own message format.
//
// Known approximation: the BinaryQuit warning line is written as its own
// dest block (see writeBinaryQuitWarning), reusing the same inter-block
// separator rule as writeBinaryMessage. In the (untested, non-default)
// combination of Heading or context mode with a Quit-mode file that has
// real matches, this would insert a spurious separator between the last
// match and the warning that real rg does not -- real rg's warning is
// part of the exact same write sequence as the file's own lines, with no
// separator at all. Plain (non-heading, non-context) mode -- the only
// combination verified against real rg -- is unaffected: its separator
// is nil either way.
//
// dest/showPath/heading/contextEnabled exist purely to write the two rg
// message lines above through the same printer.Dest the underlying sink
// writes to, so they interleave correctly (see Finish). A caller with no
// textual output stream of its own -- the root facade -- supplies a dest
// wrapping io.Discard: the suppression/drop decisions above still apply
// (they gate what reaches Sink, which is what the facade actually reads
// back), the discarded message bytes just go nowhere. This keeps every
// binary-handling decision in ONE place rather than forking it between
// cmd/gg and the facade (see PLAN.md's "one engine" requirement).
type matchTracker struct {
	search.Sink
	matched  *atomic.Bool
	standard bool
	// invertMatchSignal is Worker.InvertMatchSignal (see its doc):
	// currently set only for cmd/gg's --files-without-match, whose
	// exit-code contribution is stats.Matched's COMPLEMENT -- see
	// matchSignal.
	invertMatchSignal bool
	binMode           search.BinaryMode
	showPath          bool
	heading           bool
	contextEnabled    bool
	dest              *printer.Dest
	// searcher is consulted live, mid-scan, by Matched/Context to
	// implement the BinaryConvert suppression rule below -- see those
	// methods' doc and search.Searcher.HasBinaryOffset's doc for why
	// Stats.Binary (only known at Finish) isn't enough on its own.
	searcher *search.Searcher
	// foundBinary/foundBinaryOffset record a NUL noteLineNUL discovered
	// inside a delivered match/context line's own bytes, past the
	// searcher's bounded upfront prefix (see SearchBytes's doc and
	// noteLineNUL's doc for why this lives here rather than in package
	// search).
	foundBinary       bool
	foundBinaryOffset int64
}

// noteLineNUL checks a just-delivered Matched/Context line's own bytes for
// a NUL and, if the searcher's bounded upfront prefix check hasn't already
// found one and this tracker hasn't either, records the offset locally.
// This is the counterpart to SearchBytes's deliberately bounded detection
// (see its doc): real rg's own mmap path (SliceByLine) never scans the
// whole file for a NUL either -- it only notices one that falls within a
// line it actually visits (matched or context). Checking m.Line/c.Line
// here, rather than making package search scan the whole slice, gives gg
// the exact same coverage rg has at effectively zero cost: one short
// memchr per delivered line, not one over the whole file.
//
// Only ever called when t.standard (from Matched/Context, mirroring
// binaryConvertSuppressed's gating) -- -c/-l don't need the offset since
// they never suppress or write the summary message.
func (t *matchTracker) noteLineNUL(lineOffset int64, line []byte) {
	if t.binMode != search.BinaryConvert || t.foundBinary || t.searcher.HasBinaryOffset() {
		return
	}
	if i := bytes.IndexByte(line, 0); i >= 0 {
		t.foundBinary = true
		t.foundBinaryOffset = lineOffset + int64(i)
	}
}

// effectiveBinaryOffset returns the NUL offset binaryConvertSuppressed and
// Finish should use: the searcher's own (bounded-prefix) detection if it
// found one, else whatever noteLineNUL discovered from a delivered line,
// else not-ok.
func (t *matchTracker) effectiveBinaryOffset() (offset int64, ok bool) {
	if t.searcher.HasBinaryOffset() {
		return t.searcher.BinaryOffset(), true
	}
	if t.foundBinary {
		return t.foundBinaryOffset, true
	}
	return 0, false
}

// binaryConvertSuppressed reports whether a match/context line spanning
// absolute byte range [lineStart, lineEnd) should be withheld from the
// underlying sink under BinaryConvert mode, matching rg's real
// explicit-file behavior exactly (empirically verified against the
// installed rg binary with fixtures placing a NUL at several offsets
// straddling DefaultBufferSize, both --mmap and --no-mmap, identical
// result both ways):
//
//   - If the detected NUL falls within the first DefaultBufferSize
//     bytes, EVERY line is suppressed -- even ones textually before the
//     NUL -- so only the one summary message (writeBinaryMessage,
//     written from Finish) appears for the whole file.
//   - Otherwise, a line is suppressed once its own byte range reaches
//     the NUL's offset -- lineEnd > binOffset, not just lineStart -- so
//     a line whose bytes straddle the NUL (SearchBytes/mmap never
//     rewrites a NUL into a line terminator the way Search's streaming
//     path does, so a line containing text on both sides of a NUL is a
//     completely ordinary occurrence, not a rare edge case) is
//     suppressed too, exactly like one that starts after it. Lines
//     entirely before the NUL still display normally, and the summary
//     message is appended after.
//
// This only ever affects the "standard" (default line-printing) sink;
// -c/-l/-q must keep counting/reporting every match regardless of where
// it falls (rg's own `-c` on such a file reports the true total, not a
// truncated one) -- callers must only invoke this when t.standard is
// already true.
func (t *matchTracker) binaryConvertSuppressed(lineEnd int64) bool {
	if t.binMode != search.BinaryConvert {
		return false
	}
	binOffset, ok := t.effectiveBinaryOffset()
	if !ok {
		return false
	}
	return binOffset < int64(search.DefaultBufferSize) || lineEnd > binOffset
}

// Matched overrides the embedded Sink to apply binaryConvertSuppressed
// before formatting a line -- see its doc. Suppressing here (rather
// than in the shared search package) is deliberate: package search
// calls every Sink's Matched/Context for every match regardless of mode
// (Count's own tally, for instance, comes from counting these calls,
// never from Stats.MatchCount -- see printer.Count's doc), so
// suppression must live above that shared path, applied only to the
// standard-mode display sink.
func (t *matchTracker) Matched(m *search.Match) (bool, error) {
	if t.standard {
		t.noteLineNUL(m.Offset, m.Line)
		if t.binaryConvertSuppressed(m.Offset + int64(len(m.Line))) {
			return true, nil
		}
	}
	return t.Sink.Matched(m)
}

// Context overrides the embedded Sink for the same reason as Matched.
func (t *matchTracker) Context(c *search.Ctx) (bool, error) {
	if t.standard {
		t.noteLineNUL(c.Offset, c.Line)
		if t.binaryConvertSuppressed(c.Offset + int64(len(c.Line))) {
			return true, nil
		}
	}
	return t.Sink.Context(c)
}

// matchSignal reports whether this file's Stats should count towards the
// overall exit-code "matched anything" signal -- stats.Matched itself for
// every mode except --files-without-match (invertMatchSignal), where the
// desired signal is the exact complement: a file this Stats came from
// contributes to a "found something" exit code (0) precisely when it had
// ZERO real matches (i.e. printer.FilesWithoutMatch printed its path),
// verified against the real rg binary (see ModeFilesWithoutMatch's doc in
// cmd/gg/flags.go).
func (t *matchTracker) matchSignal(stats *search.Stats) bool {
	if t.invertMatchSignal {
		return !stats.Matched
	}
	return stats.Matched
}

func (t *matchTracker) Finish(path string, stats *search.Stats) error {
	if t.binMode == search.BinaryQuit && stats.Binary {
		if !t.standard {
			// -c/-l: discard entirely, without even calling the
			// underlying sink's Finish (which would otherwise flush
			// whatever real count/path it already accumulated).
			return nil
		}
		if t.matchSignal(stats) {
			t.matched.Store(true)
		}
		if err := t.Sink.Finish(path, stats); err != nil {
			return err
		}
		if !stats.Matched {
			return nil
		}
		return writeBinaryQuitWarning(t.dest, path, stats.BinaryOffset, t.showPath, t.heading, t.contextEnabled)
	}
	if t.binMode == search.BinaryConvert {
		if offset, ok := t.effectiveBinaryOffset(); ok {
			// Matched/Context have already withheld anything
			// binaryConvertSuppressed flagged, so whatever the underlying
			// sink accumulated is exactly what should display normally --
			// unlike BinaryQuit above, nothing here needs discarding.
			if t.matchSignal(stats) {
				t.matched.Store(true)
			}
			if err := t.Sink.Finish(path, stats); err != nil {
				return err
			}
			if !stats.Matched || !t.standard {
				return nil
			}
			return writeBinaryMessage(t.dest, path, offset, t.showPath, t.heading, t.contextEnabled)
		}
	}
	if t.matchSignal(stats) {
		t.matched.Store(true)
	}
	return t.Sink.Finish(path, stats)
}

// binarySeparator computes the same inter-block separator
// Standard.interFileSeparator would use (heading -> blank line, context
// -> "--", else none), so a binary-related message written directly to
// dest chains correctly with neighboring file blocks.
func binarySeparator(heading, contextEnabled bool) []byte {
	switch {
	case heading:
		return []byte("\n")
	case contextEnabled:
		return []byte("--\n")
	default:
		return nil
	}
}

// writeBinaryMessage writes rg's generic binary-match line directly to
// dest, bypassing the Standard sink's own buffer/Finish entirely (see
// matchTracker.Finish). The path prefix follows the same ShowPath
// heuristic as normal output.
func writeBinaryMessage(dest *printer.Dest, path string, offset int64, showPath, heading, contextEnabled bool) error {
	var buf []byte
	if showPath {
		buf = append(buf, path...)
		buf = append(buf, ':', ' ')
	}
	buf = append(buf, `binary file matches (found "\0" byte around offset `...)
	buf = strconv.AppendInt(buf, offset, 10)
	buf = append(buf, ")\n"...)
	return dest.WriteBlock(buf, binarySeparator(heading, contextEnabled))
}

// writeBinaryQuitWarning writes rg's "stopped searching" warning line
// directly to dest, after the real matches (already flushed via the
// underlying sink's own Finish) -- see matchTracker.Finish's BinaryQuit
// branch and its doc for the verified wording and the known separator
// approximation in heading/context mode.
func writeBinaryQuitWarning(dest *printer.Dest, path string, offset int64, showPath, heading, contextEnabled bool) error {
	var buf []byte
	if showPath {
		buf = append(buf, path...)
		buf = append(buf, ':', ' ')
	}
	buf = append(buf, `WARNING: stopped searching binary file after match (found "\0" byte around offset `...)
	buf = strconv.AppendInt(buf, offset, 10)
	buf = append(buf, ")\n"...)
	return dest.WriteBlock(buf, binarySeparator(heading, contextEnabled))
}
