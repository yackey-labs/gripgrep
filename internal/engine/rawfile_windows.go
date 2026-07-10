package engine

import "os"

// rawFile on Windows is just an *os.File. The unix implementation
// (rawfile_unix.go) exists to skip os.newFile's runtime-poller
// registration -- a Linux-profiled fcntl round trip with no direct
// Windows equivalent -- so there's nothing to bypass here, and os.Open
// already produces the *fs.PathError shape (fs.ErrNotExist et al.) gg's
// error reporting relies on.
type rawFile struct {
	*os.File
}

// openRaw opens path for reading. See rawfile_unix.go for the contract;
// on Windows this is a plain os.Open.
func openRaw(path string) (*rawFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &rawFile{f}, nil
}

// disableEOFHint is a no-op: the short-read-implies-EOF hint (M3 #28,
// see rawfile_unix.go) is a unix-rawFile optimization justified by
// Linux's uninterruptible regular-file read(2) semantics. os.File.Read
// never skips a confirm read, so this rawFile permanently behaves as if
// the hint were disabled -- which is exactly what wire.go's explicit-arg
// opt-out asks for.
func (f *rawFile) disableEOFHint() {}
