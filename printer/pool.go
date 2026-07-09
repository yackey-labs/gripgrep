package printer

import "sync"

// Buffer sizing policy: workers start with a small buffer and grow it
// (via append) as needed for whatever a file's output requires. A file
// with unusually large output can leave a worker holding an oversized
// buffer indefinitely, so the pool caps what it will keep: buffers
// larger than maxPooledCap are dropped (left for GC) rather than reused,
// and a Printer releases an oversized buffer back through the same path
// at the start of its next file (see resetBuf).
const (
	initialBufCap = 4 << 10  // 4KB
	maxPooledCap  = 64 << 10 // 64KB
)

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, initialBufCap)
		return &b
	},
}

// getBuf returns a zero-length buffer from the pool (or a fresh one).
func getBuf() []byte {
	bp := bufPool.Get().(*[]byte)
	b := *bp
	return b[:0]
}

// putBuf returns b to the pool, unless it has grown past the bounded
// pool policy, in which case it is dropped so GC reclaims it instead of
// every future pool consumer paying for one file's outlier.
func putBuf(b []byte) {
	if cap(b) > maxPooledCap {
		return
	}
	b = b[:0]
	bufPool.Put(&b)
}

// resetBuf prepares buf for a new file: a lazy reset via buf[:0] in the
// common case, or a full release-and-reacquire when buf has grown
// beyond the pool's bounded-size policy.
func resetBuf(buf []byte) []byte {
	if cap(buf) > maxPooledCap {
		putBuf(buf)
		return getBuf()
	}
	return buf[:0]
}
