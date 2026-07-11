package gripgrep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/internal/engine"
	"github.com/yackey-labs/gripgrep/printer"
)

// Search runs pattern over paths (or "." if none given) with CLI-default
// Options and returns every match. Equivalent to Options{}.Search.
func Search(pattern string, paths ...string) ([]Match, error) {
	return Options{}.Search(pattern, paths...)
}

// SearchContext is Search with cancellation: when ctx is done the search
// stops promptly (see the package doc's cancellation note and
// docs/library.md) and returns (nil, err) with errors.Is(err,
// context.Canceled) or context.DeadlineExceeded true -- never partial
// results. Equivalent to Options{}.SearchContext.
func SearchContext(ctx context.Context, pattern string, paths ...string) ([]Match, error) {
	return Options{}.SearchContext(ctx, pattern, paths...)
}

// FilesWithMatch returns the sorted-by-discovery-order (nondeterministic
// across runs, like gg -l's own parallel walk) list of paths containing
// at least one match, with CLI-default Options. Equivalent to
// Options{}.FilesWithMatch.
func FilesWithMatch(pattern string, paths ...string) ([]string, error) {
	return Options{}.FilesWithMatch(pattern, paths...)
}

// FilesWithMatchContext is FilesWithMatch with cancellation; see
// SearchContext for the semantics and Options{}.FilesWithMatchContext.
func FilesWithMatchContext(ctx context.Context, pattern string, paths ...string) ([]string, error) {
	return Options{}.FilesWithMatchContext(ctx, pattern, paths...)
}

// CountMatches returns a map from path to match count, one entry per
// file that matched at least once, with CLI-default Options. Equivalent
// to Options{}.CountMatches.
func CountMatches(pattern string, paths ...string) (map[string]int, error) {
	return Options{}.CountMatches(pattern, paths...)
}

// CountMatchesContext is CountMatches with cancellation; see SearchContext
// for the semantics and Options{}.CountMatchesContext.
func CountMatchesContext(ctx context.Context, pattern string, paths ...string) (map[string]int, error) {
	return Options{}.CountMatchesContext(ctx, pattern, paths...)
}

// Files lists every file that would be searched under paths (or "." if
// none given), honoring gitignore/hidden-file filtering exactly like the
// CLI's --files -- without matching anything. There is no Options
// variant: Files only cares about which flags shape the walked file set
// (Hidden/NoIgnore/Globs/MaxFilesize/Workers), so it takes those as
// plain arguments instead of forcing a caller to build a whole Options
// value for the ones that don't apply. Use Options{Hidden: ...}.filesConfig
// indirectly via SearchStream-style helpers if finer control is ever
// needed; for now this mirrors the CLI's own --files, which has no
// pattern-related flags to speak of either.
func Files(paths ...string) ([]string, error) {
	return FilesContext(context.Background(), paths...)
}

// FilesContext is Files with cancellation: when ctx is done the walk stops
// at the next directory/file boundary and returns (nil, err) with
// errors.Is(err, context.Canceled)/DeadlineExceeded true -- no partial
// list. See Files for the (identical) filtering behavior.
func FilesContext(ctx context.Context, paths ...string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	econf := engine.Config{Paths: paths}
	var mu sync.Mutex
	var out []string
	var stopped atomic.Bool
	stop, cleanup := watchCancel(ctx, &stopped)
	defer cleanup()
	visit := func(path string) {
		mu.Lock()
		out = append(out, strings.Clone(path))
		mu.Unlock()
	}
	errW := newErrCollector()
	if _, err := engine.FilesStopping(econf, visit, stop, errW); err != nil {
		return out, err
	}
	return collectingReturn(ctx, out, errW.err())
}

// Search runs pattern over paths (or "." if none given) with o's
// options and returns every match, in nondeterministic (parallel walk)
// order -- like gg's own default output order, which is never sorted.
func (o Options) Search(pattern string, paths ...string) ([]Match, error) {
	return o.SearchContext(context.Background(), pattern, paths...)
}

// SearchContext is Search with cancellation. When ctx is done, the search
// stops promptly (observed at directory, file, and intra-file chunk
// boundaries -- see docs/library.md's Cancellation section for the honest
// granularity) and returns (nil, err) with errors.Is(err,
// context.Canceled)/DeadlineExceeded true: no partial slice is ever
// returned on cancellation. A ctx that is never cancelled (the
// context.Background() the non-ctx Search delegates with) is byte-for-byte
// identical to that older verb, including its return of any collected
// per-file error alongside the partial matches gathered before it.
func (o Options) SearchContext(ctx context.Context, pattern string, paths ...string) ([]Match, error) {
	var mu sync.Mutex
	var out []Match
	err := o.SearchStreamContext(ctx, pattern, paths, func(m Match) bool {
		mu.Lock()
		out = append(out, m)
		mu.Unlock()
		return true
	})
	if err != nil && ctx.Err() != nil {
		return nil, err
	}
	return out, err
}

// SearchStream runs pattern over paths with o's options, calling fn once
// per match as soon as it's known (which, when o.After or o.Context
// requests trailing context, is after that many further lines have been
// read -- see Match.After's doc). fn may be called concurrently from
// multiple goroutines (files are searched in parallel, like every other
// verb in this package); it must synchronize its own side effects if it
// has any beyond the delivered Match.
//
// Returning false from fn stops the search as soon as practical: the
// current file's remaining search aborts immediately, and no further
// file's search is started (though any already in flight on another
// goroutine may still deliver one more match before observing the stop
// request -- an unavoidable consequence of the parallel walk, not a bug
// to work around).
func SearchStream(pattern string, paths []string, fn func(Match) bool) error {
	return Options{}.SearchStream(pattern, paths, fn)
}

// SearchStreamContext is SearchStream with cancellation. Once ctx is done,
// fn is invoked exactly zero more times -- each delivery is guarded by a
// synchronous ctx.Err() check and deliveries are serialized, so a callback
// that cancels its own ctx is never called again, regardless of scheduler
// timing. The call returns an error with errors.Is(err, context.Canceled)/
// DeadlineExceeded true (wrapping any per-file errors collected during the
// walk, so both are visible). Cancellation also rides the early-stop flag
// fn's own false return sets, stopping the walk before each visited
// directory/file and -- on the intra-file parallel path -- as chunk replay
// stops delivering. It is not instantaneous mid-scan; see docs/library.md's
// Cancellation section.
func SearchStreamContext(ctx context.Context, pattern string, paths []string, fn func(Match) bool) error {
	return Options{}.SearchStreamContext(ctx, pattern, paths, fn)
}

// SearchStream is the Options-driven form of the package-level
// SearchStream; see its doc.
func (o Options) SearchStream(pattern string, paths []string, fn func(Match) bool) error {
	return o.SearchStreamContext(context.Background(), pattern, paths, fn)
}

// SearchStreamContext is the Options-driven form of the package-level
// SearchStreamContext; see its doc for the cancellation contract.
func (o Options) SearchStreamContext(ctx context.Context, pattern string, paths []string, fn func(Match) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return err
	}
	before, after := resolveContext(o)

	var deliverMu sync.Mutex
	var stopped atomic.Bool
	stop, cleanup := watchCancel(ctx, &stopped)
	defer cleanup()

	// On a cancellable ctx, guard every delivery with a synchronous
	// ctx.Err() check: because match delivery is serialized under
	// deliverMu, a callback that cancels its own ctx is happens-before
	// ordered with the very next delivery's check, so fn is never invoked
	// again once cancellation is observed -- exactly zero post-cancel
	// callbacks, independent of the watcher goroutine's latency (the
	// watcher still stops the WALK when cancellation arrives from outside a
	// callback). A never-cancellable ctx skips the wrap entirely, so the
	// delegating non-ctx SearchStream pays nothing and is unchanged.
	emit := fn
	if ctx.Done() != nil {
		emit = func(m Match) bool {
			if ctx.Err() != nil {
				return false
			}
			return fn(m)
		}
	}

	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink: &matchCollector{
				before: before, after: after,
				matcher: matcher,
				emit:    emit, mu: &deliverMu, stopped: &stopped,
			},
			Standard: true,
		}
	}

	errW := newErrCollector()
	if _, err = engine.Run(econf, newWorker, nil, stop, discardBinaryMessaging(), nil, errW); err != nil {
		return err
	}
	return cancelOr(ctx, errW.err())
}

// FilesWithMatch returns the list of paths containing at least one
// match, using o's options. See the package-level FilesWithMatch for the
// CLI-default form.
func (o Options) FilesWithMatch(pattern string, paths ...string) ([]string, error) {
	return o.FilesWithMatchContext(context.Background(), pattern, paths...)
}

// FilesWithMatchContext is FilesWithMatch with cancellation; see
// SearchContext for the (identical-shaped) semantics: (nil, err) on
// cancellation, partial-plus-error on an un-cancelled ctx.
func (o Options) FilesWithMatchContext(ctx context.Context, pattern string, paths ...string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	var out []string
	var stopped atomic.Bool
	stop, cleanup := watchCancel(ctx, &stopped)
	defer cleanup()
	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink:     &pathListSink{mu: &mu, out: &out, stopped: &stopped},
			Standard: false,
		}
	}

	errW := newErrCollector()
	if _, err := engine.Run(econf, newWorker, nil, stop, discardBinaryMessaging(), nil, errW); err != nil {
		return out, err
	}
	return collectingReturn(ctx, out, errW.err())
}

// CountMatches returns a map from path to match count, one entry per
// file that matched at least once, using o's options. See the
// package-level CountMatches for the CLI-default form.
func (o Options) CountMatches(pattern string, paths ...string) (map[string]int, error) {
	return o.CountMatchesContext(context.Background(), pattern, paths...)
}

// CountMatchesContext is CountMatches with cancellation; see SearchContext
// for the semantics: (nil, err) on cancellation, partial map plus error on
// an un-cancelled ctx.
func (o Options) CountMatchesContext(ctx context.Context, pattern string, paths ...string) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	out := make(map[string]int)
	var stopped atomic.Bool
	stop, cleanup := watchCancel(ctx, &stopped)
	defer cleanup()
	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink:     &countingSink{mu: &mu, out: out, stopped: &stopped},
			Standard: false,
		}
	}

	errW := newErrCollector()
	if _, err := engine.Run(econf, newWorker, nil, stop, discardBinaryMessaging(), nil, errW); err != nil {
		return out, err
	}
	return collectingReturn(ctx, out, errW.err())
}

// watchCancel bridges a caller's ctx onto the existing early-stop flag the
// facade's sinks already latch when a streaming callback returns false:
// while ctx is live a watcher goroutine flips stopped the moment it is
// cancelled, so every subsequent directory/file boundary (checked by
// engine.Run/FilesStopping) and every intra-file chunk replay observes the
// stop through machinery that already existed -- no second stop channel.
// The returned stop is what engine.Run consults; cleanup must be deferred
// to retire the watcher when the call returns.
//
// A ctx that can never be cancelled -- context.Background()/TODO(), whose
// Done() channel is nil -- starts no goroutine at all: the delegating
// non-ctx verbs pay nothing and keep their exact prior behavior, since
// stopped can then only ever be flipped by a callback's own false return.
func watchCancel(ctx context.Context, stopped *atomic.Bool) (stop func() bool, cleanup func()) {
	stop = stopped.Load
	if ctx.Done() == nil {
		return stop, func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stopped.Store(true)
		case <-done:
		}
	}()
	return stop, func() { close(done) }
}

// cancelOr folds a cancelled ctx together with whatever per-file errors a
// walk collected. A live ctx.Err() always wins the returned error -- so
// errors.Is(err, context.Canceled)/DeadlineExceeded holds for callers
// keying off cancellation -- wrapping (not dropping) the collected file
// errors when both happened. With an un-cancelled ctx it returns fileErr
// unchanged, the exact value the pre-ctx verbs returned.
func cancelOr(ctx context.Context, fileErr error) error {
	cerr := ctx.Err()
	if cerr == nil {
		return fileErr
	}
	if fileErr == nil {
		return cerr
	}
	return fmt.Errorf("%w (with search errors: %v)", cerr, fileErr)
}

// collectingReturn applies the collecting verbs' result rule to a gathered
// value: on cancellation, discard it and return (zero, cancelErr) -- no
// partial results; otherwise return (out, fileErr), matching the pre-ctx
// verbs' partial-plus-error behavior on an un-cancelled ctx.
func collectingReturn[T any](ctx context.Context, out T, fileErr error) (T, error) {
	err := cancelOr(ctx, fileErr)
	if err != nil && ctx.Err() != nil {
		var zero T
		return zero, err
	}
	return out, err
}

// discardBinaryMessaging builds the engine.BinaryMessaging every facade
// verb passes to engine.Run: this package has no textual output stream
// of its own, so rg's binary-file message TEXT ("binary file matches...",
// "WARNING: stopped searching...") is discarded -- but the drop/
// suppression DECISIONS those messages accompany still apply, because
// they're made in internal/engine's matchTracker regardless of where
// Dest points (see that type's doc and internal/engine.BinaryMessaging's
// doc). This is what makes CountMatches/FilesWithMatch agree with the
// CLI's own -c/-l on a tree containing binary files without forking any
// suppression logic into this package.
func discardBinaryMessaging() engine.BinaryMessaging {
	return engine.BinaryMessaging{Dest: printer.NewDest(io.Discard)}
}

// errCollector is the io.Writer this package gives engine.Run/
// engine.Files for per-file/per-path error reporting (permission denied,
// a file deleted between readdir and open, an invalid pattern -- see
// their doc). The CLI writes these straight to the user's terminal as
// they occur; a library has no such stream, so they're collected here
// and folded into a single returned error instead of being silently
// dropped.
type errCollector struct {
	mu    sync.Mutex
	lines []string
}

func newErrCollector() *errCollector { return &errCollector{} }

func (w *errCollector) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.lines = append(w.lines, strings.TrimRight(string(p), "\n"))
	w.mu.Unlock()
	return len(p), nil
}

func (w *errCollector) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lines) == 0 {
		return nil
	}
	return errors.New(strings.Join(w.lines, "\n"))
}
