//go:build unix

package walk

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestFIFONeverOpened verifies a FIFO is classified (not TypeFile/TypeDir)
// and visiting it completes promptly — walk must never call os.Open on a
// non-directory entry, since opening a FIFO with no reader/writer on the
// other end blocks forever. Unix-only because the fixture (Mkfifo) is;
// the walker behavior it guards — never opening non-directory entries —
// is platform-independent.
func TestFIFONeverOpened(t *testing.T) {
	root := t.TempDir()
	fifoPath := filepath.Join(root, "myfifo")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	writeFile(t, filepath.Join(root, "a.txt"), "a")

	done := make(chan FileType, 1)
	go func() {
		var fifoType FileType
		Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
			if e.Path == fifoPath {
				fifoType = e.Type
			}
			return Continue
		})
		done <- fifoType
	}()

	select {
	case ft := <-done:
		if ft == TypeFile || ft == TypeDir {
			t.Errorf("FIFO classified as %v; want anything but TypeFile/TypeDir (walk must not treat it as an openable regular file)", ft)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Walk hung — a FIFO was probably opened, blocking with no reader/writer on the other end")
	}
}
