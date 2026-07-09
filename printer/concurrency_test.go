package printer

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// TestConcurrentWorkers_AtomicPerFileBlocks is the multi-worker
// interleaving test mandated by the M1-printer task: many goroutines,
// each owning its own Standard (as a real per-worker printer would),
// write concurrently to one shared Dest. It asserts each file's block
// of lines lands in the output as one contiguous, complete, in-order
// run — never torn or interleaved with another file's lines — which is
// exactly what Dest's one-Write-under-lock design is for. Run with
// -race to also catch any accidental sharing of a pooled buffer across
// goroutines.
func TestConcurrentWorkers_AtomicPerFileBlocks(t *testing.T) {
	const numWorkers = 32
	const linesPerFile = 64

	dest, out := newTestDest()

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			p := NewStandard(dest)
			path := fmt.Sprintf("file%02d.txt", w)
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
		}(w)
	}
	wg.Wait()

	raw := strings.TrimRight(out.String(), "\n")
	if raw == "" {
		t.Fatal("expected output, got none")
	}
	lines := strings.Split(raw, "\n")
	if len(lines) != numWorkers*linesPerFile {
		t.Fatalf("got %d total lines, want %d", len(lines), numWorkers*linesPerFile)
	}

	seenPaths := make(map[string]bool, numWorkers)
	for i := 0; i < len(lines); i += linesPerFile {
		block := lines[i : i+linesPerFile]
		path := fieldBefore(block[0], ':')
		if seenPaths[path] {
			t.Fatalf("file %s's block is non-contiguous (reappeared at output line %d)", path, i)
		}
		seenPaths[path] = true

		var workerNum int
		if _, err := fmt.Sscanf(path, "file%d.txt", &workerNum); err != nil {
			t.Fatalf("unparseable path %q: %v", path, err)
		}
		wantContent := fmt.Sprintf("W%d", workerNum)

		for j, line := range block {
			wantLineNo := strconv.Itoa(j + 1)
			gotPath := fieldBefore(line, ':')
			if gotPath != path {
				t.Fatalf("block starting at line %d torn: line %d belongs to %q, expected %q\nfull line: %q", i, i+j, gotPath, path, line)
			}
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				t.Fatalf("malformed line %q", line)
			}
			if parts[1] != wantLineNo {
				t.Fatalf("block for %s out of order at position %d: got line number %s, want %s", path, j, parts[1], wantLineNo)
			}
			if parts[2] != wantContent {
				t.Fatalf("block for %s content corrupted at position %d: got %q, want %q (buffer sharing race?)", path, j, parts[2], wantContent)
			}
		}
	}
	if len(seenPaths) != numWorkers {
		t.Fatalf("expected %d distinct file blocks, saw %d", numWorkers, len(seenPaths))
	}
}

// fieldBefore returns s up to (not including) the first occurrence of
// sep, or all of s if sep does not appear.
func fieldBefore(s string, sep byte) string {
	if i := strings.IndexByte(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}
