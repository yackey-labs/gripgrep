package printer

import (
	"encoding/base64"
	"io"
	"strconv"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// JSON implements rg's --json event-stream printer: for every file that
// emits at least one match/context line it writes a "begin" message, then
// one "match"/"context" message per delivered line, then an "end" message
// carrying that file's own stats -- exactly rg's grep-printer JSON sink
// (crates/printer/src/json.rs). Files with no emitted line write nothing
// (no begin/end), but still contribute to the run-level summary. The
// summary itself is a separate message written once, after the whole walk,
// by JSONAccumulator.WriteSummary -- rg emits it from crates/core/main.rs's
// print_stats, not from the printer, which is why its field order differs
// (see WriteSummary).
//
// Field order is CONTRACTUAL and deliberately inconsistent: the event
// messages here follow rg's serde struct order (path, lines, line_number,
// absolute_offset, submatches; end stats: elapsed, searches,
// searches_with_match, bytes_searched, bytes_printed, matched_lines,
// matches), while the summary is alphabetical (rg routes it through
// serde_json::json!, a BTreeMap). Everything is hand-serialized to keep
// byte-exact control over that order, over serde_json's exact string
// escaping (see appendJSONString), and over the self-referential
// bytes_printed accounting (see Finish).
//
// One JSON should be constructed per worker goroutine (NewJSON) and reused
// across files: Begin resets a private buffer and per-file counters, Finish
// flushes the whole file's messages to the shared Dest as one locked write
// (so per-file blocks are never torn or interleaved under parallel search,
// matching rg's own atomic-per-file output). A JSON's buffer is not safe
// for concurrent use. The run-level JSONAccumulator it points at IS shared
// across every worker and is the one place cross-file totals live.
type JSON struct {
	dest *Dest
	acc  *JSONAccumulator

	// Matcher locates submatch spans within each delivered line (rg's
	// record_matches). Required; a nil Matcher yields empty submatches.
	Matcher match.Matcher
	// Invert mirrors rg's searcher.invert_match(): under -v, context lines
	// get their submatches computed too (rg's context handler), where a
	// normal run leaves them empty. Match events are unaffected (an
	// inverted match line legitimately finds zero spans -- see J5).
	Invert bool
	// Quiet is rg's -q under --json: no per-file events are emitted at all,
	// but every file is still searched to completion and its stats folded
	// into the run summary, so the summary is the ONLY message written
	// (verified against the real rg binary, J8). bytes_printed stays 0.
	Quiet bool

	buf  []byte
	path []byte

	beginPrinted bool
	matchCount   int64
	matchedLines int64
	matches      int64
	startTime    time.Time

	spanScratch []matchSpan
}

// NewJSON returns a JSON printer flushing completed files to dest and
// folding per-file stats into acc (shared across every worker).
func NewJSON(dest *Dest, acc *JSONAccumulator) *JSON {
	return &JSON{dest: dest, acc: acc, buf: getBuf()}
}

var _ search.Sink = (*JSON)(nil)

// Begin implements search.Sink: resets the per-file buffer and counters and
// starts this file's wall clock (rg resets its CounterWriter and start_time
// in JSONSink::begin). The "begin" message itself is written lazily, on the
// first match/context line, so a zero-match file emits nothing.
func (p *JSON) Begin(path string) (bool, error) {
	p.buf = resetBuf(p.buf)
	p.path = append(p.path[:0], path...)
	p.beginPrinted = false
	p.matchCount = 0
	p.matchedLines = 0
	p.matches = 0
	p.startTime = time.Now()
	return true, nil
}

// Matched implements search.Sink: counts the line's occurrences (matches)
// and the line itself (matched_lines), then -- unless Quiet -- emits the
// lazy "begin" message followed by this line's "match" message. rg counts
// stats regardless of whether output is suppressed, which is why the
// counting happens above the Quiet guard.
func (p *JSON) Matched(m *search.Match) (bool, error) {
	p.matchCount++
	spans := p.computeSubmatches(m.Line)
	p.matches += int64(len(spans))
	p.matchedLines++
	if !p.Quiet {
		p.writeBeginMessage()
		p.buf = p.appendLineMessage(p.buf, "match", m.Line, m.LineNumber, m.HasLineNumber, m.Offset, spans)
	}
	return true, nil
}

// Context implements search.Sink: emits a "context" message (submatches
// empty unless Invert, mirroring rg's context handler). Context never
// touches match stats -- only Matched does.
func (p *JSON) Context(c *search.Ctx) (bool, error) {
	if p.Quiet {
		return true, nil
	}
	var spans []matchSpan
	if p.Invert {
		spans = p.computeSubmatches(c.Line)
	}
	p.writeBeginMessage()
	p.buf = p.appendLineMessage(p.buf, "context", c.Line, c.LineNumber, c.HasLineNumber, c.Offset, spans)
	return true, nil
}

// Finish implements search.Sink: folds this file's stats into the shared
// accumulator, then -- if any event was emitted -- appends the "end"
// message and flushes the whole file's block to Dest as one write.
//
// bytes_printed is self-referential: it is the exact byte length of this
// file's begin+match+context messages (each including its trailing '\n'),
// NOT counting the end message itself -- rg reads its CounterWriter's count
// in finish() BEFORE writing the end message (see json.rs), so the end
// message's own bytes never appear in any file's bytes_printed nor in the
// summed summary total. Here that count is simply len(buf) at this point,
// since the end message is appended afterward.
//
// bytes_searched is clamped to the binary offset when binary data was
// detected: rg's searcher stops at the NUL, so finish.byte_count() reports
// only the bytes before it (verified, J15: a 14-byte file with a NUL at 6
// reports bytes_searched=6). binary_offset carries that same offset into
// the end message.
func (p *JSON) Finish(path string, stats *search.Stats) error {
	elapsed := time.Since(p.startTime)
	bytesSearched := stats.BytesSearched
	if stats.Binary && stats.BinaryOffset < bytesSearched {
		// Clamp bytes_searched DOWN to the NUL offset, where rg's searcher
		// stops. This is a min(): an explicit-arg binary (convert mode) reads
		// the whole file, so BytesSearched > offset and this clamps to the
		// offset (J15: 14 -> 6, contributing the clamped bytes). A
		// walk-discovered binary (quit mode) instead quarantines the file --
		// the searcher discards the NUL-bearing chunk and reports 0 bytes
		// searched, which is already below the offset, so this leaves it at 0.
		// rg likewise attributes NO bytes to a quarantined binary: it counts
		// only searches+1 for it, never its offset (verified against the real
		// rg binary -- a walk over a text corpus plus two NUL-bearing files
		// sums bytes_searched over the text files ONLY). The two cases are
		// distinguished purely by whether gg already searched past the offset,
		// so no binary-mode plumbing into this sink is needed.
		bytesSearched = stats.BinaryOffset
	}
	bytesPrinted := int64(len(p.buf))

	p.acc.searches.Add(1)
	if p.matchCount > 0 {
		p.acc.searchesWithMatch.Add(1)
	}
	p.acc.bytesSearched.Add(bytesSearched)
	p.acc.bytesPrinted.Add(bytesPrinted)
	p.acc.matchedLines.Add(p.matchedLines)
	p.acc.matches.Add(p.matches)
	p.acc.elapsedNanos.Add(int64(elapsed))

	if !p.beginPrinted {
		return nil
	}

	searchesWithMatch := int64(0)
	if p.matchCount > 0 {
		searchesWithMatch = 1
	}
	b := p.buf
	b = append(b, `{"type":"end","data":{"path":`...)
	b = appendData(b, p.path)
	b = append(b, `,"binary_offset":`...)
	if stats.Binary {
		b = strconv.AppendInt(b, stats.BinaryOffset, 10)
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"stats":`...)
	b = appendStatsStruct(b, elapsed, 1, searchesWithMatch, bytesSearched, bytesPrinted, p.matchedLines, p.matches)
	b = append(b, "}}\n"...)
	p.buf = b
	return p.dest.WriteBlock(p.buf, nil)
}

// writeBeginMessage appends the lazy "begin" message once per file (rg's
// write_begin_message).
func (p *JSON) writeBeginMessage() {
	if p.beginPrinted {
		return
	}
	b := p.buf
	b = append(b, `{"type":"begin","data":{"path":`...)
	b = appendData(b, p.path)
	b = append(b, "}}\n"...)
	p.buf = b
	p.beginPrinted = true
}

// appendLineMessage appends one "match" or "context" message with rg's
// struct field order: path, lines, line_number, absolute_offset,
// submatches.
func (p *JSON) appendLineMessage(b []byte, kind string, line []byte, lineNumber int64, hasLineNumber bool, offset int64, spans []matchSpan) []byte {
	b = append(b, `{"type":"`...)
	b = append(b, kind...)
	b = append(b, `","data":{"path":`...)
	b = appendData(b, p.path)
	b = append(b, `,"lines":`...)
	b = appendData(b, line)
	b = append(b, `,"line_number":`...)
	if hasLineNumber {
		b = strconv.AppendInt(b, lineNumber, 10)
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"absolute_offset":`...)
	b = strconv.AppendInt(b, offset, 10)
	b = append(b, `,"submatches":[`...)
	for i, sp := range spans {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"match":`...)
		b = appendData(b, line[sp.s:sp.e])
		b = append(b, `,"start":`...)
		b = strconv.AppendInt(b, int64(sp.s), 10)
		b = append(b, `,"end":`...)
		b = strconv.AppendInt(b, int64(sp.e), 10)
		b = append(b, '}')
	}
	b = append(b, "]}}\n"...)
	return b
}

// computeSubmatches locates every match span on line and applies rg's
// "don't report an empty match at the very end of the bytes" rule
// (record_matches pops a trailing empty match whose start >= len(line)).
// The scan runs over the FULL delivered line INCLUDING its terminator --
// matching rg's mat.bytes(), so an empty pattern reports one span per
// position from 0 up to (but not including) the length, e.g. 17 spans for
// a 17-byte line (J16). The returned slice aliases p.spanScratch; it is
// valid only until the next computeSubmatches call.
func (p *JSON) computeSubmatches(line []byte) []matchSpan {
	p.spanScratch = findMatchSpans(p.spanScratch[:0], p.Matcher, line)
	spans := p.spanScratch
	if n := len(spans); n > 0 && spans[n-1].s == spans[n-1].e && spans[n-1].s >= len(line) {
		spans = spans[:n-1]
	}
	return spans
}

// appendData serializes a byte slice as rg's Data type: {"text":<escaped>}
// when the bytes are valid UTF-8, {"bytes":<standard base64>} otherwise
// (crates/printer/src/jsont.rs's Data::from_bytes/from_path). Used for both
// path and line content, per-field, so a single event can mix text and
// bytes (J13).
func appendData(b, data []byte) []byte {
	if utf8.Valid(data) {
		b = append(b, `{"text":"`...)
		b = appendJSONString(b, data)
		return append(b, `"}`...)
	}
	b = append(b, `{"bytes":"`...)
	n := base64.StdEncoding.EncodedLen(len(data))
	start := len(b)
	b = append(b, make([]byte, n)...)
	base64.StdEncoding.Encode(b[start:], data)
	return append(b, `"}`...)
}

const jsonHex = "0123456789abcdef"

// appendJSONString escapes s into a JSON string body (no surrounding
// quotes) byte-for-byte as serde_json's default serializer does: '"' and
// '\' are backslash-escaped; the control chars \b \t \n \f \r use their
// short forms; every other byte below 0x20 becomes \u00XX with LOWERCASE
// hex; everything else -- including bytes >= 0x80 (valid UTF-8 continuation
// bytes) and the HTML-significant <, >, & -- is emitted verbatim. This
// differs from Go's encoding/json, which escapes <, >, & by default and
// escapes U+2028/U+2029 even with SetEscapeHTML(false); hand-rolling avoids
// both divergences. s must be valid UTF-8 (appendData only takes this path
// for valid UTF-8).
func appendJSONString(b, s []byte) []byte {
	for _, c := range s {
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\t':
			b = append(b, '\\', 't')
		case '\n':
			b = append(b, '\\', 'n')
		case '\f':
			b = append(b, '\\', 'f')
		case '\r':
			b = append(b, '\\', 'r')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', jsonHex[c>>4], jsonHex[c&0xf])
			} else {
				b = append(b, c)
			}
		}
	}
	return b
}

// appendStatsStruct appends an end-message stats object in rg's serde
// struct order (crates/printer/src/stats.rs): elapsed, searches,
// searches_with_match, bytes_searched, bytes_printed, matched_lines,
// matches. The nested elapsed uses NiceDuration's own struct order (secs,
// nanos, human).
func appendStatsStruct(b []byte, elapsed time.Duration, searches, searchesWithMatch, bytesSearched, bytesPrinted, matchedLines, matches int64) []byte {
	b = append(b, `{"elapsed":`...)
	b = appendNiceDurationStruct(b, elapsed)
	b = append(b, `,"searches":`...)
	b = strconv.AppendInt(b, searches, 10)
	b = append(b, `,"searches_with_match":`...)
	b = strconv.AppendInt(b, searchesWithMatch, 10)
	b = append(b, `,"bytes_searched":`...)
	b = strconv.AppendInt(b, bytesSearched, 10)
	b = append(b, `,"bytes_printed":`...)
	b = strconv.AppendInt(b, bytesPrinted, 10)
	b = append(b, `,"matched_lines":`...)
	b = strconv.AppendInt(b, matchedLines, 10)
	b = append(b, `,"matches":`...)
	b = strconv.AppendInt(b, matches, 10)
	return append(b, '}')
}

// appendNiceDurationStruct appends a NiceDuration in serde struct order
// (secs, nanos, human) -- the form used inside END-message stats.
func appendNiceDurationStruct(b []byte, d time.Duration) []byte {
	b = append(b, `{"secs":`...)
	b = strconv.AppendInt(b, int64(d/time.Second), 10)
	b = append(b, `,"nanos":`...)
	b = strconv.AppendInt(b, int64(d%time.Second), 10)
	b = append(b, `,"human":"`...)
	b = appendHuman(b, d)
	return append(b, `"}`...)
}

// appendNiceDurationAlpha appends a NiceDuration in ALPHABETICAL key order
// (human, nanos, secs) -- the form the summary uses, since rg builds the
// summary through serde_json::json! (a BTreeMap).
func appendNiceDurationAlpha(b []byte, d time.Duration) []byte {
	b = append(b, `{"human":"`...)
	b = appendHuman(b, d)
	b = append(b, `","nanos":`...)
	b = strconv.AppendInt(b, int64(d%time.Second), 10)
	b = append(b, `,"secs":`...)
	b = strconv.AppendInt(b, int64(d/time.Second), 10)
	return append(b, '}')
}

// appendHuman appends rg's NiceDuration Display form, "{:0.6}s" of the
// duration's fractional seconds. Test normalizers replace this value, so
// its precise rounding is not load-bearing, only its shape.
func appendHuman(b []byte, d time.Duration) []byte {
	b = strconv.AppendFloat(b, d.Seconds(), 'f', 6, 64)
	return append(b, 's')
}

// JSONAccumulator holds the run-level totals the --json summary reports,
// summed across every file and every parallel worker (each worker's JSON
// sink folds its per-file stats in at Finish). Every field is atomic:
// workers running concurrently all add into the same instance, exactly
// like rg's aggregate Stats. Created once per invocation; read once, after
// the walk completes, by WriteSummary.
type JSONAccumulator struct {
	searches          atomic.Int64
	searchesWithMatch atomic.Int64
	bytesSearched     atomic.Int64
	bytesPrinted      atomic.Int64
	matchedLines      atomic.Int64
	matches           atomic.Int64
	elapsedNanos      atomic.Int64
}

// NewJSONAccumulator returns a fresh, all-zero accumulator.
func NewJSONAccumulator() *JSONAccumulator {
	return &JSONAccumulator{}
}

// WriteSummary writes the single "summary" message to w, after the walk has
// completed (so no worker is still folding stats in). Its shape mirrors rg's
// print_stats JSON arm exactly: data BEFORE type, and every object's keys
// ALPHABETICAL (rg builds it via serde_json::json!, a BTreeMap), which is
// the opposite of the struct-ordered end message. elapsedTotal is the
// caller's whole-run wall clock (rg's own started-at measurement); the
// summed per-file elapsed comes from the accumulator.
func (a *JSONAccumulator) WriteSummary(w io.Writer, elapsedTotal time.Duration) error {
	var b []byte
	b = append(b, `{"data":{"elapsed_total":`...)
	b = appendNiceDurationAlpha(b, elapsedTotal)
	b = append(b, `,"stats":{"bytes_printed":`...)
	b = strconv.AppendInt(b, a.bytesPrinted.Load(), 10)
	b = append(b, `,"bytes_searched":`...)
	b = strconv.AppendInt(b, a.bytesSearched.Load(), 10)
	b = append(b, `,"elapsed":`...)
	b = appendNiceDurationAlpha(b, time.Duration(a.elapsedNanos.Load()))
	b = append(b, `,"matched_lines":`...)
	b = strconv.AppendInt(b, a.matchedLines.Load(), 10)
	b = append(b, `,"matches":`...)
	b = strconv.AppendInt(b, a.matches.Load(), 10)
	b = append(b, `,"searches":`...)
	b = strconv.AppendInt(b, a.searches.Load(), 10)
	b = append(b, `,"searches_with_match":`...)
	b = strconv.AppendInt(b, a.searchesWithMatch.Load(), 10)
	b = append(b, `}},"type":"summary"}`...)
	b = append(b, '\n')
	_, err := w.Write(b)
	return err
}
