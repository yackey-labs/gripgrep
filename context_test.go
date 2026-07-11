package gripgrep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeMatchTree writes nDirs directories of nFiles files each, every file
// holding linesPerFile lines that all contain "needle", under a fresh
// temp root. Enough files that an uncancelled walk is measurably slower
// than one cancelled at the first entry.
func makeMatchTree(t *testing.T, nDirs, nFiles, linesPerFile int) string {
	t.Helper()
	root := t.TempDir()
	body := strings.Repeat("a needle in here\n", linesPerFile)
	for d := 0; d < nDirs; d++ {
		dir := filepath.Join(root, "d"+itoa(d))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < nFiles; f++ {
			p := filepath.Join(dir, "f"+itoa(f)+".txt")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// makeBigMatchFile writes a single file just over the 64MiB intra-file
// parallel threshold, every line a match, so the facade's parallel search
// path is engaged and its match-replay stream is large.
func makeBigMatchFile(t *testing.T) string {
	t.Helper()
	const line = "needle on this line\n" // 20 bytes
	const target = 80 << 20              // 80 MiB > 64 MiB threshold
	root := t.TempDir()
	p := filepath.Join(root, "big.txt")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat(line, 1<<16) // ~1.25 MiB per write
	for written := 0; written < target; written += len(chunk) {
		if _, err := f.WriteString(chunk); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestContextDelegationMatchesNonContext pins the delegation contract:
// every *Context verb driven by context.Background() returns exactly what
// its pre-ctx sibling does on the same fixture.
func TestContextDelegationMatchesNonContext(t *testing.T) {
	root := makeMatchTree(t, 4, 3, 2)
	ctx := context.Background()

	t.Run("Search", func(t *testing.T) {
		want, err := Search("needle", root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SearchContext(ctx, "needle", root)
		if err != nil {
			t.Fatal(err)
		}
		if !sameMatches(want, got) {
			t.Fatalf("Search vs SearchContext differ: %d vs %d matches", len(want), len(got))
		}
	})

	t.Run("FilesWithMatch", func(t *testing.T) {
		want, err := FilesWithMatch("needle", root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := FilesWithMatchContext(ctx, "needle", root)
		if err != nil {
			t.Fatal(err)
		}
		if !sameStrings(want, got) {
			t.Fatalf("FilesWithMatch vs Context differ:\n%v\n%v", want, got)
		}
	})

	t.Run("CountMatches", func(t *testing.T) {
		want, err := CountMatches("needle", root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CountMatchesContext(ctx, "needle", root)
		if err != nil {
			t.Fatal(err)
		}
		if len(want) != len(got) {
			t.Fatalf("count map sizes differ: %d vs %d", len(want), len(got))
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("count for %s differ: %d vs %d", k, v, got[k])
			}
		}
	})

	t.Run("Files", func(t *testing.T) {
		want, err := Files(root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := FilesContext(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if !sameStrings(want, got) {
			t.Fatalf("Files vs FilesContext differ:\n%v\n%v", want, got)
		}
	})

	t.Run("SearchStream", func(t *testing.T) {
		var mu sync.Mutex
		var a, b []Match
		if err := SearchStream("needle", []string{root}, func(m Match) bool {
			mu.Lock()
			a = append(a, m)
			mu.Unlock()
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if err := SearchStreamContext(ctx, "needle", []string{root}, func(m Match) bool {
			mu.Lock()
			b = append(b, m)
			mu.Unlock()
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if !sameMatches(a, b) {
			t.Fatalf("SearchStream vs Context differ: %d vs %d", len(a), len(b))
		}
	})
}

// TestContextAlreadyCancelled: a ctx cancelled before the call returns the
// ctx error immediately, with no results, no callbacks, no I/O beyond
// setup.
func TestContextAlreadyCancelled(t *testing.T) {
	root := makeMatchTree(t, 4, 3, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if m, err := SearchContext(ctx, "needle", root); m != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchContext: got (%v, %v), want (nil, Canceled)", m, err)
	}
	if l, err := FilesWithMatchContext(ctx, "needle", root); l != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("FilesWithMatchContext: got (%v, %v)", l, err)
	}
	if c, err := CountMatchesContext(ctx, "needle", root); c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CountMatchesContext: got (%v, %v)", c, err)
	}
	if l, err := FilesContext(ctx, root); l != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("FilesContext: got (%v, %v)", l, err)
	}

	var calls atomic.Int64
	err := SearchStreamContext(ctx, "needle", []string{root}, func(m Match) bool {
		calls.Add(1)
		return true
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchStreamContext err = %v, want Canceled", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("callback fired %d times on already-cancelled ctx, want 0", n)
	}
}

// TestContextCancelFromCallbackParallel drives the >64MiB parallel path and
// cancels from inside the callback on the very first match. Replay stops
// at once: the callback fires exactly once (no further callbacks), the
// call returns the ctx error, and it finishes far below a full replay of
// the file's millions of matching lines.
func TestContextCancelFromCallbackParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 80MiB single-file cancellation test in -short")
	}
	path := makeBigMatchFile(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var calls int
	start := time.Now()
	err := SearchStreamContext(ctx, "needle", []string{path}, func(m Match) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		cancel()
		return false
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("callback fired %d times, want exactly 1 (no further callbacks after cancel)", got)
	}
	// Generous: the whole file (80MiB, ~4M matching lines) would take far
	// longer to fully replay; a stop at the first match must return well
	// inside this bound.
	if elapsed > 30*time.Second {
		t.Fatalf("cancelled single-file search took %v, expected prompt return", elapsed)
	}
	t.Logf("parallel single-file cancel: %v to first-match stop", elapsed)
}

// TestContextCancelFromCallbackTree drives a many-small-matches tree and
// cancels from inside the callback on its first invocation while returning
// TRUE (so nothing but the ctx cancellation can stop delivery). The
// synchronous per-delivery ctx.Err() guard, plus serialized delivery, must
// hold the callback to exactly one invocation despite the watcher
// goroutine's asynchronous latch -- the regression the single-file,
// return-false test could not see.
func TestContextCancelFromCallbackTree(t *testing.T) {
	root := makeMatchTree(t, 40, 10, 5) // 400 files, 2000 matching lines

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var calls int
	err := SearchStreamContext(ctx, "needle", []string{root}, func(m Match) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		cancel()
		return true // only the ctx cancellation may stop the stream
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("callback fired %d times, want exactly 1 (no post-cancel callbacks)", got)
	}
}

// TestContextCancelTreeWalkPrompt: an immediately-cancelled walk over a big
// tree returns well under an uncancelled walk of the same tree.
func TestContextCancelTreeWalkPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive tree cancellation test in -short")
	}
	root := makeMatchTree(t, 200, 20, 3) // 4000 files

	start := time.Now()
	full, err := SearchContext(context.Background(), "needle", root)
	if err != nil {
		t.Fatal(err)
	}
	uncancelled := time.Since(start)
	if len(full) == 0 {
		t.Fatal("expected matches in the fixture")
	}
	if uncancelled < 5*time.Millisecond {
		t.Skipf("uncancelled walk too fast to measure reliably (%v)", uncancelled)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go cancel() // fire as the walk gets going
	start = time.Now()
	got, err := SearchContext(ctx, "needle", root)
	cancelled := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Fatalf("cancelled SearchContext returned %d matches, want nil (no partial results)", len(got))
	}
	if cancelled >= uncancelled {
		t.Fatalf("cancelled walk (%v) not faster than uncancelled (%v)", cancelled, uncancelled)
	}
	t.Logf("tree walk: uncancelled %v, cancelled %v", uncancelled, cancelled)
}

// TestContextDeadlineExceeded: a deadline that fires mid-walk surfaces as a
// DeadlineExceeded error through errors.Is.
func TestContextDeadlineExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive deadline test in -short")
	}
	root := makeMatchTree(t, 200, 20, 3)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline is already past

	if m, err := SearchContext(ctx, "needle", root); m != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SearchContext with expired deadline: got (%d matches, %v), want (nil, DeadlineExceeded)", len(m), err)
	}
}

func sameMatches(a, b []Match) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(m Match) string {
		return m.Path + "\x00" + itoa(m.LineNumber) + "\x00" + m.Line
	}
	ka := make([]string, len(a))
	kb := make([]string, len(b))
	for i := range a {
		ka[i] = key(a[i])
	}
	for i := range b {
		kb[i] = key(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
