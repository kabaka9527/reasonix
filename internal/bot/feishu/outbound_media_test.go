package feishu

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"reasonix/internal/config"
)

func TestReadOutboundFileConfinement(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(inside, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &adapter{cfg: config.FeishuBotConfig{OutboundMediaRoots: []string{root}}}

	data, name, err := a.readOutboundFile(inside)
	if err != nil {
		t.Fatalf("file inside root should be readable: %v", err)
	}
	if string(data) != "hello" || name != "ok.txt" {
		t.Fatalf("got %q/%q, want hello/ok.txt", data, name)
	}

	// Outside every root: rejected.
	if _, _, err := a.readOutboundFile(outside); err == nil {
		t.Fatal("file outside the roots must be rejected")
	}

	// Traversal that resolves outside the root: rejected.
	if _, _, err := a.readOutboundFile(filepath.Join(root, "..", filepath.Base(outside))); err == nil {
		t.Fatal("traversal out of the root must be rejected")
	}

	// Relative path: rejected.
	if _, _, err := a.readOutboundFile("relative/path"); err == nil {
		t.Fatal("relative path must be rejected")
	}

	// No roots configured: local sending disabled.
	off := &adapter{cfg: config.FeishuBotConfig{}}
	if _, _, err := off.readOutboundFile(inside); err == nil {
		t.Fatal("local file sending must be disabled when no roots are set")
	}
}

func TestReadOutboundFileRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is unreliable on Windows CI")
	}
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink living inside the allowed root but pointing outside it.
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	a := &adapter{cfg: config.FeishuBotConfig{OutboundMediaRoots: []string{root}}}
	if _, _, err := a.readOutboundFile(link); err == nil {
		t.Fatal("a symlink escaping the root must be rejected (symlink resolution)")
	}
}

func TestReadOutboundFileAcceptsSymlinkWithinRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is unreliable on Windows CI")
	}
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	a := &adapter{cfg: config.FeishuBotConfig{OutboundMediaRoots: []string{root}}}
	if _, _, err := a.readOutboundFile(link); err != nil {
		t.Fatalf("a symlink staying within the root should be allowed: %v", err)
	}
}
