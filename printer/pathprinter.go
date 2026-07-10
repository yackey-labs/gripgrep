package printer

// PathPrinter implements --files: it does not participate in
// search.Sink at all, since --files mode skips the matcher/searcher
// entirely and only walks (see PLAN.md's Output row). It runs one
// dedicated goroutine fed by a channel of discovered paths and writes
// each as "path\n" to a Dest, batching several paths per write to keep
// syscall count down on large trees without holding unbounded memory.
type PathPrinter struct {
	dest  *Dest
	paths chan string
	done  chan struct{}
	color bool
}

// batchSize bounds how many paths PathPrinter accumulates before
// flushing, independent of the byte-size cap in flush.
const pathBatchSize = 256

// NewPathPrinter starts the printer's goroutine, which reads paths sent
// on Paths() and writes them to dest until that channel is closed. color
// enables coloring each path (matching rg's own `--files --color=always`
// behavior, verified against the real rg binary -- it does colorize
// --files output, the same magenta used for paths everywhere else); it
// is a constructor parameter rather than a field set after construction
// because run's goroutine starts immediately and reads it on every path,
// so setting it later would race.
func NewPathPrinter(dest *Dest, color bool) *PathPrinter {
	p := &PathPrinter{
		dest:  dest,
		paths: make(chan string, pathBatchSize),
		done:  make(chan struct{}),
		color: color,
	}
	go p.run()
	return p
}

// Paths returns the channel to send discovered paths to (typically from
// a walk.Visitor). Close it once walking is complete, then call Wait.
func (p *PathPrinter) Paths() chan<- string {
	return p.paths
}

// Wait blocks until the printer goroutine has drained every path sent
// on Paths() and flushed it to Dest. Callers must close the Paths
// channel first, or Wait blocks forever.
func (p *PathPrinter) Wait() {
	<-p.done
}

func (p *PathPrinter) run() {
	defer close(p.done)
	buf := getBuf()
	n := 0
	for path := range p.paths {
		if p.color {
			buf = appendColoredBytes(buf, ansiPath, []byte(path))
		} else {
			buf = append(buf, path...)
		}
		buf = append(buf, '\n')
		n++
		if n >= pathBatchSize || len(buf) >= maxPooledCap {
			p.dest.Write(buf)
			buf = buf[:0]
			n = 0
		}
	}
	if len(buf) > 0 {
		p.dest.Write(buf)
	}
	putBuf(buf)
}
