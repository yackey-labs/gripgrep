//go:build e2e

// Golden e2e coverage for the wiring quartet: -L/--follow,
// --one-file-system, --no-messages, and --no-ignore-messages. Every case
// runs BOTH the pinned rg 15.1.0 binary and gg with -j1 in an isolated
// HOME/XDG (t.TempDir), comparing stdout byte-for-byte and exit codes
// exactly; stderr text is gg-flavored, so only its presence/absence is
// asserted (the contractual part). Ported from the lead's answer-key probes
// -- probe scripts ran against the pinned rg binary. Reuses the
// helpers defined in e2e_ignore_test.go (buildGG, cmpIgnore, newIgnoreEnv,
// writeFile, mkdirAll, e2eRoot).
package gripgrep_test

import (
	"os"
	"path/filepath"
	"testing"
)

// symlink creates oldname<-newname, skipping the whole test if the platform
// doesn't support symlinks (Windows without privilege).
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
}

// TestGoldenFollowLinks covers the L-block: default symlink skipping, -L
// following (files + dirs), --no-follow negation, explicit-arg following,
// content search through -L, symlink loops (error + exit 2, --no-messages-
// gated), and broken links.
func TestGoldenFollowLinks(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }

	// L1-L5, L11: a tree whose entries symlink to files/dirs OUTSIDE it.
	base := t.TempDir()
	real := filepath.Join(base, "real")
	tree := filepath.Join(base, "tree")
	writeFile(t, filepath.Join(real, "target.txt"), "needle\n")
	writeFile(t, filepath.Join(tree, "plain.txt"), "needle\n")
	symlink(t, "../real", filepath.Join(tree, "dirlink"))
	symlink(t, "../real/target.txt", filepath.Join(tree, "filelink.txt"))

	for _, tc := range []struct {
		name string
		cwd  string
		args []string
	}{
		{"L1_default_no_follow", tree, files()},
		{"L2_follow_both", tree, files("-L")},
		{"L3_follow_then_no_follow", tree, files("-L", "--no-follow")},
		{"L4_explicit_symlink_arg", tree, files("dirlink")},
		{"L5_search_through_follow", tree, []string{"-j1", "-n", "-L", "needle"}},
		{"L11_follow_files_dirlink_arg", tree, files("-L", "dirlink")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, tc.cwd, env, tc.args) })
	}

	// L6-L8: a symlink loop (a/b/back -> a). Under -L this is an error
	// (exit 2, one stderr line), suppressible via --no-messages while the
	// exit code stays 2.
	loop := t.TempDir()
	mkdirAll(t, filepath.Join(loop, "a", "b"))
	writeFile(t, filepath.Join(loop, "a", "f.txt"), "needle\n")
	symlink(t, "../../a", filepath.Join(loop, "a", "b", "back"))
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"L6_loop_files", files("-L")},
		{"L7_loop_with_match", []string{"-j1", "-l", "-L", "needle"}},
		{"L8_loop_no_messages", []string{"-j1", "-l", "-L", "--no-messages", "needle"}},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, loop, env, tc.args) })
	}

	// L9-L10: a broken symlink. Silently skipped by default (exit 0); an IO
	// error under -L (exit 2, one stderr line).
	broken := t.TempDir()
	symlink(t, "nowhere", filepath.Join(broken, "dead.txt"))
	writeFile(t, filepath.Join(broken, "live.txt"), "needle\n")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"L9_broken_default", files()},
		{"L10_broken_follow", files("-L")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, broken, env, tc.args) })
	}
}

// TestGoldenMessages covers the M-block: --no-messages / --messages over
// unreadable files and directories, and --no-ignore-messages / --no-messages
// over ignore-file load warnings. The unreadable cases chmod 000, which does
// nothing as root -- skipped there.
func TestGoldenMessages(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)

	// M1-M6: an unreadable regular file. Error + exit 2 (with or without a
	// match); --no-messages suppresses the line but never the exit code;
	// --files never reads the file, so it lists fine (exit 0, no message).
	t.Run("unreadable_file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not block root")
		}
		msg := t.TempDir()
		writeFile(t, filepath.Join(msg, "ok.txt"), "needle\n")
		writeFile(t, filepath.Join(msg, "secret.txt"), "needle\n")
		if err := os.Chmod(filepath.Join(msg, "secret.txt"), 0o000); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"M1_error_with_match", []string{"-j1", "-l", "needle"}},
			{"M2_no_messages", []string{"-j1", "-l", "--no-messages", "needle"}},
			{"M3_no_messages_then_messages", []string{"-j1", "-l", "--no-messages", "--messages", "needle"}},
			{"M4_error_no_match", []string{"-j1", "-l", "absent"}},
			{"M5_no_match_no_messages", []string{"-j1", "-l", "--no-messages", "absent"}},
			{"M6_files_unreadable_listed", []string{"--files", "-j1"}},
		} {
			t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, msg, env, tc.args) })
		}
	})

	// M12-M13: an unreadable directory encountered during the walk. Error +
	// exit 2; --no-messages suppresses the line but not the exit code.
	t.Run("unreadable_dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not block root")
		}
		udir := t.TempDir()
		writeFile(t, filepath.Join(udir, "ok.txt"), "needle\n")
		locked := filepath.Join(udir, "locked")
		mkdirAll(t, locked)
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"M12_unreadable_dir", []string{"-j1", "-l", "needle"}},
			{"M13_unreadable_dir_no_messages", []string{"-j1", "-l", "--no-messages", "needle"}},
		} {
			t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, udir, env, tc.args) })
		}
	})

	// M7-M9: an unreadable tree .gitignore. rg 15 prints NOTHING and exits 0
	// (gg's loadGlobSet is already silent) -- a regression guard that it
	// stays that way, with and without the message-suppression flags.
	t.Run("unreadable_gitignore", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not block root")
		}
		imsg := t.TempDir()
		mkdirAll(t, filepath.Join(imsg, ".git"))
		writeFile(t, filepath.Join(imsg, "ok.txt"), "needle\n")
		gi := filepath.Join(imsg, ".gitignore")
		writeFile(t, gi, "junk\n")
		if err := os.Chmod(gi, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(gi, 0o644) })
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"M7_unreadable_gitignore", []string{"-j1", "-l", "needle"}},
			{"M8_no_ignore_messages", []string{"-j1", "-l", "--no-ignore-messages", "needle"}},
			{"M9_no_messages", []string{"-j1", "-l", "--no-messages", "needle"}},
		} {
			t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, imsg, env, tc.args) })
		}
	})

	// M10-M11: a missing --ignore-file warns to stderr and never touches the
	// exit code (a real match still gives exit 0). Either --no-ignore-messages
	// OR --no-messages silences the warning.
	t.Run("missing_ignore_file", func(t *testing.T) {
		imsg := t.TempDir()
		mkdirAll(t, filepath.Join(imsg, ".git"))
		writeFile(t, filepath.Join(imsg, "ok.txt"), "needle\n")
		missing := filepath.Join(t.TempDir(), "nope")
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"M10_no_ignore_messages", []string{"-j1", "-l", "--no-ignore-messages", "--ignore-file", missing, "needle"}},
			{"M11_no_messages", []string{"-j1", "-l", "--no-messages", "--ignore-file", missing, "needle"}},
			// Control: WITHOUT the flags, rg warns (stderr present) -- proves
			// the suppression above is real, not a fixture that never warns.
			{"M10c_warns_without_flag", []string{"-j1", "-l", "--ignore-file", missing, "needle"}},
		} {
			t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, imsg, env, tc.args) })
		}
	})
}
