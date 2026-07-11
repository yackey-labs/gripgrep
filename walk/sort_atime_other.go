//go:build !unix

package walk

import (
	"os"
	"time"
)

// accessTime has no portable non-Unix implementation here; --sort accessed
// falls back to reporting no time (files then sort last, ascending) on such
// platforms. gg's parity target is Unix, where the build tag above selects
// the real implementation.
func accessTime(os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
