//go:build darwin

package walk

import (
	"os"
	"syscall"
	"time"
)

// accessTime returns the last-access time (atime) from a Unix stat result.
// os.FileInfo exposes only ModTime directly, so atime comes from the
// underlying syscall.Stat_t. Note atime is subject to the mount's
// relatime/noatime policy -- rg reads the same field and inherits the same
// caveat, so this is faithful for --sort accessed parity.
func accessTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Atimespec.Unix()), true
}
