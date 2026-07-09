package printer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// recordingWriter records each Write call's bytes as an independent,
// copied chunk (io.Writer must not retain the slice past the call, so
// this defensively copies rather than aliasing it). Used to observe
// Dest's actual write boundaries directly, rather than inferring them
// from concatenated output — the strongest possible check that each
// worker's Finish produces exactly one atomic write.
type recordingWriter struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	r.mu.Lock()
	r.chunks = append(r.chunks, cp)
	r.mu.Unlock()
	return len(p), nil
}

// TestConcurrentWorkers_AtomicPerFileBlocks is the multi-worker
// interleaving test mandated by the M1-printer task and reinforced by
// the lead's binding test-coverage mandate ("must assert per-file block
// atomicity byte-exactly"): many goroutines, each owning its own
// Standard (as a real per-worker printer would), write concurrently to
// one shared Dest. Rather than inferring atomicity from concatenated
// output, this records Dest's actual Write() calls and asserts there is
// exactly one per file, each byte-for-byte equal to that file's
// complete expected block — proof that no write was ever torn or
// interleaved with another worker's, not just that the final output
// happens to look right. Run with -race (make test does this
// unconditionally) to also catch any accidental sharing of a pooled
// buffer across goroutines.
func TestConcurrentWorkers_AtomicPerFileBlocks(t *testing.T) {
	const numWorkers = 32
	const linesPerFile = 64

	rw := &recordingWriter{}
	dest := NewDest(rw)

	expected := make([]string, numWorkers)
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		var b []byte
		path := fmt.Sprintf("file%02d.txt", w)
		for i := 1; i <= linesPerFile; i++ {
			b = append(b, path...)
			b = append(b, ':')
			b = append(b, fmt.Sprintf("%d", i)...)
			b = append(b, ':')
			b = append(b, fmt.Sprintf("W%d", w)...)
			b = append(b, '\n')
		}
		expected[w] = string(b)

		wg.Add(1)
		go func(w int, path string) {
			defer wg.Done()
			p := NewStandard(dest)
			if _, err := p.Begin(path); err != nil {
				t.Error(err)
				return
			}
			for i := 1; i <= linesPerFile; i++ {
				content := fmt.Sprintf("W%d", w)
				if _, err := p.Matched(&search.Match{
					Line:          []byte(content),
					LineNumber:    int64(i),
					HasLineNumber: true,
				}); err != nil {
					t.Error(err)
					return
				}
			}
			if err := p.Finish(path, &search.Stats{Matched: true, MatchCount: linesPerFile}); err != nil {
				t.Error(err)
			}
		}(w, path)
	}
	wg.Wait()

	if len(rw.chunks) != numWorkers {
		t.Fatalf("got %d Write calls, want exactly %d (one atomic write per file — a torn or merged write means the lock/buffer protocol is broken)", len(rw.chunks), numWorkers)
	}

	// Each chunk must equal exactly one file's expected block,
	// byte-for-byte, and every file must be accounted for exactly once
	// (no duplicate, no missing, no cross-contamination between
	// workers' buffers).
	matchedTo := make([]bool, numWorkers)
	for ci, chunk := range rw.chunks {
		found := -1
		for w, want := range expected {
			if matchedTo[w] {
				continue
			}
			if string(chunk) == want {
				found = w
				break
			}
		}
		if found == -1 {
			t.Fatalf("write chunk %d matches no worker's expected block byte-for-byte (torn/corrupted write):\n%q", ci, chunk)
		}
		matchedTo[found] = true
	}
	for w, ok := range matchedTo {
		if !ok {
			t.Errorf("worker %d's block never appeared as its own atomic write", w)
		}
	}
}
