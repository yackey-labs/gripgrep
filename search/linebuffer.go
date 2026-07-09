package search

import (
	"bytes"
	"io"
)

// DefaultBufferSize is the default rolling read buffer capacity, matching
// rg's DEFAULT_BUFFER_CAPACITY. It is a tunable: M3 sweeps 128/256KB.
const DefaultBufferSize = 64 * 1024

// lineBuffer is a pooled, per-Searcher rolling read buffer that always
// holds only complete lines (buf[pos:lastLineTerm]); bytes in
// [lastLineTerm:end] are a partial trailing line still being filled. It
// is a straight port of ripgrep's line_buffer.rs LineBuffer.
type lineBuffer struct {
	buf  []byte
	pos  int
	last int // lastLineTerm
	end  int

	absoluteOffset int64

	binaryMode      BinaryMode
	hasBinaryOffset bool
	binaryOffset    int64
}

func newLineBuffer(capacity int) *lineBuffer {
	return &lineBuffer{buf: make([]byte, capacity)}
}

// reset prepares the buffer for a new file/reader.
func (lb *lineBuffer) reset(mode BinaryMode) {
	lb.pos = 0
	lb.last = 0
	lb.end = 0
	lb.absoluteOffset = 0
	lb.hasBinaryOffset = false
	lb.binaryOffset = 0
	lb.binaryMode = mode
}

// buffer returns the currently readable, complete-lines-only region.
func (lb *lineBuffer) buffer() []byte { return lb.buf[lb.pos:lb.last] }

func (lb *lineBuffer) freeBuffer() []byte { return lb.buf[lb.end:] }

// consume marks amt bytes (<= len(buffer())) as processed and no longer
// needed. It does not physically move memory; roll() does that lazily on
// the next fill().
func (lb *lineBuffer) consume(amt int) {
	lb.pos += amt
	lb.absoluteOffset += int64(amt)
}

// roll copies the unconsumed tail (buf[pos:end]) down to offset 0. After
// roll, last == end, and pos == 0. Idempotent.
func (lb *lineBuffer) roll() {
	if lb.pos == lb.end {
		lb.pos, lb.last, lb.end = 0, 0, 0
		return
	}
	n := lb.end - lb.pos
	copy(lb.buf[0:n], lb.buf[lb.pos:lb.end])
	lb.pos = 0
	lb.last = n
	lb.end = n
}

// ensureCapacity doubles the buffer if there is no free space left, so a
// single line longer than the configured capacity can still be read in
// full (rg's BufferAllocation::Eager — no configured cap in v1).
func (lb *lineBuffer) ensureCapacity() {
	if len(lb.freeBuffer()) > 0 {
		return
	}
	n := len(lb.buf)
	if n == 0 {
		n = 1
	}
	newBuf := make([]byte, len(lb.buf)+n)
	copy(newBuf, lb.buf)
	lb.buf = newBuf
}

// fill discards the consumed prefix (roll), then reads more data from r
// until at least one complete line is available or EOF/binary-quit is
// reached. It returns whether buffer() is non-empty (there is data to
// process); false means truly done (real EOF, nothing left to search).
func (lb *lineBuffer) fill(r io.Reader) (bool, error) {
	// Once binary-quit has fired, never read again; just report whatever
	// is left in the (already truncated) buffer.
	if lb.binaryMode == BinaryQuit && lb.hasBinaryOffset {
		return len(lb.buffer()) > 0, nil
	}

	lb.roll()
	for {
		lb.ensureCapacity()
		n, err := r.Read(lb.freeBuffer())
		if n > 0 {
			oldEnd := lb.end
			lb.end += n
			chunk := lb.buf[oldEnd:lb.end]

			switch lb.binaryMode {
			case BinaryQuit:
				if i := bytes.IndexByte(chunk, 0); i >= 0 {
					lb.end = oldEnd + i
					lb.last = lb.end
					lb.hasBinaryOffset = true
					lb.binaryOffset = lb.absoluteOffset + int64(lb.end)
					return lb.pos < lb.end, nil
				}
			case BinaryConvert:
				if !lb.hasBinaryOffset {
					// Record the exact offset of the first NUL before it
					// gets overwritten by the replacement below.
					if i := bytes.IndexByte(chunk, 0); i >= 0 {
						lb.hasBinaryOffset = true
						lb.binaryOffset = lb.absoluteOffset + int64(oldEnd+i)
					}
				}
				replaceNULInPlace(chunk)
			}

			if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
				lb.last = oldEnd + i + 1
				return true, nil
			}
			// No line terminator yet: this is a long line. Keep reading
			// unless the reader also signaled an error/EOF below.
		}
		if err != nil {
			if err == io.EOF {
				lb.last = lb.end
				return len(lb.buffer()) > 0, nil
			}
			return false, err
		}
		if n == 0 {
			// A well-behaved io.Reader shouldn't return (0, nil), but
			// tolerate it rather than spin forever on a bad one: treat as
			// EOF (io.Reader docs discourage relying on n==0 => EOF, but
			// there's nothing else we can safely do here).
			lb.last = lb.end
			return len(lb.buffer()) > 0, nil
		}
	}
}

// replaceNULInPlace replaces every NUL byte in b with '\n', using a
// run-aware fast path for consecutive NULs (binary data tends to have
// long NUL runs, so avoid restarting IndexByte for every single byte).
// Ported from rg's line_buffer.rs replace_bytes.
func replaceNULInPlace(b []byte) ([]byte, bool) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return b, false
	}
	b[i] = '\n'
	j := i + 1
	for {
		k := bytes.IndexByte(b[j:], 0)
		if k < 0 {
			break
		}
		b[j+k] = '\n'
		j += k + 1
		for j < len(b) && b[j] == 0 {
			b[j] = '\n'
			j++
		}
	}
	return b, true
}
