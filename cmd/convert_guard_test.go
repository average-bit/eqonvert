package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// withGuardState saves and restores the package-level flags the guard reads,
// so tests don't leak state into each other or the real CLI.
func withGuardState(t *testing.T, out string, force bool) {
	t.Helper()
	prevOut, prevForce := outputDir, forceExport
	outputDir, forceExport = out, force
	t.Cleanup(func() { outputDir, forceExport = prevOut, prevForce })
}

// makeSource creates a throwaway file to act as a distinct export input.
func makeSource(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGuard_EmptyDirAllowedThenStamps(t *testing.T) {
	out := t.TempDir()
	src := makeSource(t, "discA.iso")
	withGuardState(t, out, false)

	if err := guardOutputDir(src); err != nil {
		t.Fatalf("empty dir should be allowed: %v", err)
	}
	stampOutputDir(src)
	if _, ok := readManifest(out); !ok {
		t.Fatal("stamp should have written a manifest")
	}
}

func TestGuard_SameSourceReRunAllowed(t *testing.T) {
	out := t.TempDir()
	src := makeSource(t, "discA.iso")
	withGuardState(t, out, false)

	// First export.
	if err := guardOutputDir(src); err != nil {
		t.Fatal(err)
	}
	stampOutputDir(src)
	// Simulate output from the first run.
	if err := os.WriteFile(filepath.Join(out, "model.glb"), []byte("m"), 0644); err != nil {
		t.Fatal(err)
	}
	// Same source again — must be silently allowed (the fix-and-re-export loop).
	if err := guardOutputDir(src); err != nil {
		t.Fatalf("same-source re-run should be allowed: %v", err)
	}
}

func TestGuard_DifferentSourceRefused(t *testing.T) {
	out := t.TempDir()
	srcA := makeSource(t, "discA.iso")
	srcB := makeSource(t, "discB.iso")
	withGuardState(t, out, false)

	if err := guardOutputDir(srcA); err != nil {
		t.Fatal(err)
	}
	stampOutputDir(srcA)

	// Different source into the same folder — must be refused.
	if err := guardOutputDir(srcB); err == nil {
		t.Fatal("different-source export should be refused without --force")
	}
}

func TestGuard_ForceOverridesDifferentSource_PreservesManifest(t *testing.T) {
	out := t.TempDir()
	srcA := makeSource(t, "discA.iso")
	srcB := makeSource(t, "discB.iso")

	// First export from A.
	withGuardState(t, out, false)
	if err := guardOutputDir(srcA); err != nil {
		t.Fatal(err)
	}
	stampOutputDir(srcA)

	// Force B in.
	withGuardState(t, out, true)
	if err := guardOutputDir(srcB); err != nil {
		t.Fatalf("--force should override the different-source guard: %v", err)
	}
	stampOutputDir(srcB)

	// The original identity (A) must be preserved, not repointed to B.
	m, ok := readManifest(out)
	if !ok {
		t.Fatal("manifest missing after forced merge")
	}
	absA, _ := sourceIdentity(srcA)
	if m.Source != absA {
		t.Fatalf("forced merge repointed manifest to %q; want original %q", m.Source, absA)
	}
}

func TestGuard_NonEmptyNoManifestRefusedWhenExplicit(t *testing.T) {
	out := t.TempDir()
	src := makeSource(t, "discA.iso")
	// Pre-existing content, no manifest (a folder that predates the feature).
	if err := os.WriteFile(filepath.Join(out, "stale.glb"), []byte("s"), 0644); err != nil {
		t.Fatal(err)
	}
	withGuardState(t, out, false)

	if err := guardOutputDir(src); err == nil {
		t.Fatal("non-empty dir with no manifest should be refused when -o is explicit")
	}

	// --force lets it through.
	withGuardState(t, out, true)
	if err := guardOutputDir(src); err != nil {
		t.Fatalf("--force should allow writing into a non-empty unmarked dir: %v", err)
	}
}

func TestGuard_DefaultDirNonEmptyAllowed(t *testing.T) {
	// When -o is NOT given (outputDir==""), a non-empty "." must not be blocked
	// just for being non-empty (only a conflicting manifest would block).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("j"), 0644); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	src := makeSource(t, "discA.iso")
	withGuardState(t, "", false) // default output dir

	if err := guardOutputDir(src); err != nil {
		t.Fatalf("default (.) non-empty dir with no conflicting manifest should be allowed: %v", err)
	}
}
