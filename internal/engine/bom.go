package engine

import "io"

// utf8BOM is the 3-byte UTF-8 byte-order-mark rg strips before searching
// or printing a file's content, unconditionally -- verified against the
// real rg binary: `rg -a pat file-with-bom` still strips it under
// --text, so this isn't tied to binary detection at all.
var utf8BOM = [3]byte{0xEF, 0xBB, 0xBF}

// stripUTF8BOM returns a reader over r's content with a leading UTF-8
// BOM removed, if present, matching rg's own BOM-sniffing. Every other
// byte is passed through unchanged and offsets/line numbers computed by
// search.Searcher end up relative to the BOM-stripped stream, exactly
// like rg's (verified: rg's reported line 1 starts at the byte
// immediately following the BOM, as if it were never in the file at
// all).
func stripUTF8BOM(r io.Reader) (io.Reader, error) {
	return &bomReader{r: r}, nil
}

// bomReader folds the BOM check into the caller's own first Read call
// instead of issuing a dedicated 3-byte probe read first: search.Searcher
// always reads through a lineBuffer sized in tens of KB (see
// DefaultBufferSize), so the first Read this ever sees is already asking
// for a chunk far larger than 3 bytes, and the underlying reader (a
// regular file) fills as much of it as it can in one read(2) call. Round
// #27's profiling found the old separate io.ReadFull-of-3-bytes probe
// costing one whole extra read(2) syscall per file walked (~79k of them
// on the linux benchmark tree) for no reason: the BOM question can always
// be answered from bytes the caller was going to read anyway.
//
// Read-chunk boundaries here are semantically load-bearing, not just a
// performance nicety: search.Searcher's binary detection (BinaryQuit)
// discards an entire freshly-read chunk, not just the bytes at/after a
// NUL within it (see linebuffer.go's fill), so any wrapper that
// artificially splits what would have been one read(2) call into two
// separate Read results moves a later NUL into a different chunk than an
// unwrapped read would ever produce -- previously caught by
// TestGoldenVsRipgrep/invert_match, where a 3-byte leading fragment of a
// walk-discovered binary file was wrongly treated as its own clean,
// NUL-free line. The fast path below (n >= 3 on the very first Read)
// never splits anything: it reads directly into the caller's own buffer
// and, if a BOM is present, shifts the remainder down in place -- the
// caller sees exactly the read boundaries the underlying file would have
// produced unwrapped, minus the 3 BOM bytes.
type bomReader struct {
	r    io.Reader
	done bool // true once the one-time BOM check has happened
}

func (br *bomReader) Read(b []byte) (int, error) {
	if br.done {
		return br.r.Read(b)
	}
	br.done = true

	if len(b) < 3 {
		// A caller-supplied buffer under 3 bytes -- never happens via
		// search.Searcher's own tens-of-KB buffers, but stay correct
		// regardless of caller. Fall back to the small-buffer path below.
		return br.finishBOMCheck(b, nil, nil)
	}

	n, err := br.r.Read(b)
	if n < 3 {
		// A short first read from r itself (a file under 3 bytes, or a
		// reader that simply didn't fill the buffer): a BOM could still
		// be split across this boundary, so fall back to gathering up to
		// 3 bytes before deciding.
		return br.finishBOMCheck(b, b[:n], err)
	}
	if [3]byte(b[:3]) == utf8BOM {
		rest := n - 3
		if rest == 0 && err == nil {
			// The chunk r.Read just filled contained nothing but the
			// BOM -- returning (0, nil) here would be a valid but
			// discouraged io.Reader response (see io.Reader's doc), so
			// fold forward into a real read instead of handing the
			// caller an empty, ambiguous result.
			return br.r.Read(b)
		}
		copy(b, b[3:n])
		return rest, err
	}
	return n, err
}

// finishBOMCheck handles the rare case where the BOM question couldn't be
// answered from a single caller-buffer-sized Read: already holds whatever
// bytes were already read into b by the caller's own Read call, if any.
// It gathers up to 3 bytes total (or stops at EOF/error), matching what
// the old peek-based implementation did, then folds whatever isn't part
// of a BOM back into the stream via prefixReader so later Reads are
// indistinguishable from an unwrapped read. Performance doesn't matter on
// this path -- it's only reachable for a file under 3 bytes, a reader
// that returns short reads for its own reasons, or (never via this
// package's own callers) a destination buffer smaller than 3 bytes.
func (br *bomReader) finishBOMCheck(b, already []byte, err error) (int, error) {
	var buf [3]byte
	got := copy(buf[:], already)
	for got < 3 && err == nil {
		var m int
		m, err = br.r.Read(buf[got:3])
		got += m
	}
	data := buf[:got]
	if got == 3 && buf == utf8BOM {
		data = nil
	}
	if len(data) == 0 {
		if err != nil {
			return 0, err
		}
		return br.r.Read(b)
	}
	n := copy(b, data)
	if n < len(data) {
		br.r = &prefixReader{prefix: data[n:], r: br.r}
	}
	return n, err
}

// prefixReader prepends prefix to r's stream without altering r's own
// read-boundary behavior: the first Read call copies prefix into the
// destination buffer and, if room remains, also reads from r to fill the
// rest. Only used by bomReader.finishBOMCheck's rare small-buffer
// fallback -- the common path never needs it, see bomReader's doc.
type prefixReader struct {
	prefix []byte
	r      io.Reader
}

func (p *prefixReader) Read(b []byte) (int, error) {
	if len(p.prefix) == 0 {
		return p.r.Read(b)
	}
	n := copy(b, p.prefix)
	p.prefix = p.prefix[n:]
	if n == len(b) {
		return n, nil
	}
	m, err := p.r.Read(b[n:])
	return n + m, err
}
