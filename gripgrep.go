package gripgrep

import (
	"errors"
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

// FilesWithMatch returns the sorted-by-discovery-order (nondeterministic
// across runs, like gg -l's own parallel walk) list of paths containing
// at least one match, with CLI-default Options. Equivalent to
// Options{}.FilesWithMatch.
func FilesWithMatch(pattern string, paths ...string) ([]string, error) {
	return Options{}.FilesWithMatch(pattern, paths...)
}

// CountMatches returns a map from path to match count, one entry per
// file that matched at least once, with CLI-default Options. Equivalent
// to Options{}.CountMatches.
func CountMatches(pattern string, paths ...string) (map[string]int, error) {
	return Options{}.CountMatches(pattern, paths...)
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
	econf := engine.Config{Paths: paths}
	var mu sync.Mutex
	var out []string
	errW := newErrCollector()
	visit := func(path string) {
		mu.Lock()
		out = append(out, strings.Clone(path))
		mu.Unlock()
	}
	if _, err := engine.Files(econf, visit, errW); err != nil {
		return out, err
	}
	return out, errW.err()
}

// Search runs pattern over paths (or "." if none given) with o's
// options and returns every match, in nondeterministic (parallel walk)
// order -- like gg's own default output order, which is never sorted.
func (o Options) Search(pattern string, paths ...string) ([]Match, error) {
	var mu sync.Mutex
	var out []Match
	err := o.SearchStream(pattern, paths, func(m Match) bool {
		mu.Lock()
		out = append(out, m)
		mu.Unlock()
		return true
	})
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

// SearchStream is the Options-driven form of the package-level
// SearchStream; see its doc.
func (o Options) SearchStream(pattern string, paths []string, fn func(Match) bool) error {
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return err
	}
	before, after := resolveContext(o)

	var deliverMu sync.Mutex
	var stopped atomic.Bool
	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink: &matchCollector{
				before: before, after: after,
				matcher: matcher,
				emit:    fn, mu: &deliverMu, stopped: &stopped,
			},
			Standard: true,
		}
	}

	errW := newErrCollector()
	_, err = engine.Run(econf, newWorker, nil, stopped.Load, discardBinaryMessaging(), nil, errW)
	if err != nil {
		return err
	}
	return errW.err()
}

// FilesWithMatch returns the list of paths containing at least one
// match, using o's options. See the package-level FilesWithMatch for the
// CLI-default form.
func (o Options) FilesWithMatch(pattern string, paths ...string) ([]string, error) {
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	var out []string
	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink:     &pathListSink{mu: &mu, out: &out},
			Standard: false,
		}
	}

	errW := newErrCollector()
	if _, err := engine.Run(econf, newWorker, nil, nil, discardBinaryMessaging(), nil, errW); err != nil {
		return out, err
	}
	return out, errW.err()
}

// CountMatches returns a map from path to match count, one entry per
// file that matched at least once, using o's options. See the
// package-level CountMatches for the CLI-default form.
func (o Options) CountMatches(pattern string, paths ...string) (map[string]int, error) {
	econf := o.toEngineConfig(pattern, paths)
	matcher, err := engine.BuildMatcher(econf)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	out := make(map[string]int)
	newWorker := func() *engine.Worker {
		return &engine.Worker{
			Searcher: engine.NewSearcher(econf, matcher),
			Sink:     &countingSink{mu: &mu, out: out},
			Standard: false,
		}
	}

	errW := newErrCollector()
	if _, err := engine.Run(econf, newWorker, nil, nil, discardBinaryMessaging(), nil, errW); err != nil {
		return out, err
	}
	return out, errW.err()
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
