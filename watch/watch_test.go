package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFingerprint_StableWhenUnchanged verifies that fingerprint is
// deterministic: two calls over the same unchanged tree produce the same hash.
func TestFingerprint_StableWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "util.go"), "package main\n")

	first := fingerprint([]string{dir}, ".go")
	second := fingerprint([]string{dir}, ".go")
	if first != second {
		t.Fatalf("fingerprint not stable: %q != %q", first, second)
	}
}

// TestFingerprint_ChangesOnModTime verifies that touching a watched file
// changes the fingerprint (fingerprint hashes path + modtime).
func TestFingerprint_ChangesOnModTime(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	writeFile(t, f, "package main\n")

	before := fingerprint([]string{dir}, ".go")

	// Bump modtime into the future so the change is detectable regardless of
	// filesystem timestamp granularity.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	after := fingerprint([]string{dir}, ".go")
	if before == after {
		t.Fatal("fingerprint did not change after modtime bump")
	}
}

// TestFingerprint_IgnoresNonMatchingExt verifies that files whose extension
// does not match are excluded from the fingerprint.
func TestFingerprint_IgnoresNonMatchingExt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	base := fingerprint([]string{dir}, ".go")

	// Adding a non-.go file must not change the .go fingerprint.
	writeFile(t, filepath.Join(dir, "README.md"), "# docs\n")
	after := fingerprint([]string{dir}, ".go")
	if base != after {
		t.Fatal("fingerprint changed after adding a non-matching-extension file")
	}
}

// TestFingerprint_ChangesWhenFileAdded verifies that adding a new watched file
// changes the fingerprint.
func TestFingerprint_ChangesWhenFileAdded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	before := fingerprint([]string{dir}, ".go")
	writeFile(t, filepath.Join(dir, "extra.go"), "package main\n")
	after := fingerprint([]string{dir}, ".go")
	if before == after {
		t.Fatal("fingerprint did not change after adding a .go file")
	}
}

// TestFingerprint_SkipsHiddenDirs verifies that dot-directories (e.g. .git)
// are skipped by the walk.
func TestFingerprint_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	base := fingerprint([]string{dir}, ".go")

	hidden := filepath.Join(dir, ".git")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	writeFile(t, filepath.Join(hidden, "hook.go"), "package main\n")

	after := fingerprint([]string{dir}, ".go")
	if base != after {
		t.Fatal("fingerprint changed after adding a .go file inside a hidden dir")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
