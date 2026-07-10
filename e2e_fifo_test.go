//go:build e2e && unix

// The explicit-FIFO regression test lives apart from e2e_test.go's
// golden matrix because its fixture (syscall.Mkfifo) doesn't exist on
// Windows; the behavior it guards -- an explicit path argument being
// read to completion without the short-read-implies-EOF hint -- is
// itself unix-shaped (the hint only exists in the unix rawFile).
package gripgrep_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestGoldenVsRipgrep_ExplicitFIFOArg is a regression test for M3 #28: a
// FIFO passed as an explicit path argument (the same shape as a shell
// process substitution, `gg pat <(cmd)`) must still be read to
// completion, matching rg exactly. A first pass at #28's
// short-read-implies-EOF hint applied it unconditionally to every
// walk.TypeFile entry, including explicit roots that walk.buildRootTask
// never verifies as genuinely regular -- caught by hand
// (`rg hello <(cat f)` matched, `gg hello <(cat f)` printed nothing)
// before this test existed; see rawfile.go's disableEOFHint doc for the
// fix (explicit args opt out of the hint entirely). The write side
// splits its payload into two delayed chunks so the read side sees a
// genuine short read mid-stream, the exact shape the hint must not
// misinterpret as EOF here.
func TestGoldenVsRipgrep_ExplicitFIFOArg(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	content := []byte("first chunk needle_marker line\nsecond chunk plain line\nthird chunk needle_marker again\n")

	runOverFIFO := func(t *testing.T, bin string) (stdout []byte, code int) {
		t.Helper()
		fifoPath := filepath.Join(t.TempDir(), "input.fifo")
		if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}

		cmd := exec.Command(bin, "-n", "needle_marker", fifoPath)
		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf

		writeDone := make(chan error, 1)
		go func() {
			// Opening for write blocks until the child process's own
			// open(2) for read on this same path rendezvous with it.
			w, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
			if err != nil {
				writeDone <- err
				return
			}
			defer w.Close()
			mid := len(content) / 2
			if _, err := w.Write(content[:mid]); err != nil {
				writeDone <- err
				return
			}
			time.Sleep(20 * time.Millisecond)
			_, err = w.Write(content[mid:])
			writeDone <- err
		}()

		runErr := cmd.Run()
		if werr := <-writeDone; werr != nil {
			t.Fatalf("writing to fifo: %v", werr)
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if runErr != nil {
			t.Fatalf("running %s: %v", bin, runErr)
		}
		return outBuf.Bytes(), code
	}

	rgOut, rgCode := runOverFIFO(t, "rg")
	ggOut, ggCode := runOverFIFO(t, ggBin)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("explicit-FIFO-argument output mismatch:\n--- rg ---\n%s\n--- gg ---\n%s", rgOut, ggOut)
	}
	if len(ggOut) == 0 {
		t.Error("gg produced no output for a FIFO passed as an explicit path argument")
	}
}
