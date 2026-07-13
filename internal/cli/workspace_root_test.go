package cli

import (
	"path/filepath"
	"testing"
)

// TestWorkspaceRootForDir covers the --dir plumbing: no --dir yields no override
// (empty, so boot falls back to git-root detection), while a --dir run returns
// the post-chdir working directory as the explicit root. A Getwd failure surfaces
// as an error rather than a silent empty fallback.
func TestWorkspaceRootForDir(t *testing.T) {
	// No --dir: no explicit override, no error.
	if got, err := workspaceRootForDir(""); err != nil || got != "" {
		t.Fatalf("workspaceRootForDir(\"\") = %q, %v; want \"\", nil", got, err)
	}

	// With --dir: chdirTo has already switched in, so the root is the CWD.
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := workspaceRootForDir(dir)
	if err != nil {
		t.Fatalf("workspaceRootForDir: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("workspaceRootForDir returned unusable path %q: %v", got, err)
	}
	if gotResolved != want {
		t.Fatalf("workspaceRootForDir(%q) = %q, want cwd %q", dir, got, dir)
	}
}
