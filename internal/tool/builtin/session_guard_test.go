package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/sandbox"
)

// stateRootFor builds a fake Reasonix state root with the two guarded session
// trees populated, returning the root and one file path in each tree.
func stateRootFor(t *testing.T) (root, cliSession, projectSession string) {
	t.Helper()
	root = t.TempDir()
	cliSession = filepath.Join(root, "sessions", "20260707-abc.jsonl")
	projectSession = filepath.Join(root, "projects", "-Users-me-proj", "sessions", "20260707-def.jsonl")
	for _, p := range []string{cliSession, projectSession} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, cliSession, projectSession
}

func TestSessionDataGuardDeniesSessionStores(t *testing.T) {
	root, cliSession, projectSession := stateRootFor(t)
	g := NewSessionDataGuard(root, nil)

	for _, target := range []string{
		cliSession,
		projectSession,
		filepath.Join(root, "sessions", "sub", "new.jsonl"),                     // not-yet-existing file under the store
		filepath.Join(root, "projects", "any-slug", "sessions", "x.jsonl.meta"), // CAS ledger sidecar
	} {
		if err := g.Check(target); err == nil {
			t.Errorf("Check(%q) = nil, want session-data denial", target)
		} else if !strings.Contains(err.Error(), "session data") {
			t.Errorf("Check(%q) error %q does not name session data", target, err)
		}
	}
}

func TestSessionDataGuardAllowsOrdinaryStatePaths(t *testing.T) {
	root, _, _ := stateRootFor(t)
	g := NewSessionDataGuard(root, nil)

	for _, target := range []string{
		filepath.Join(root, "config.toml"),                        // config is confine()'s job, not this guard's
		filepath.Join(root, "projects", "slug", "memory", "a.md"), // memory files are not session data
		filepath.Join(root, "skills", "demo", "SKILL.md"),
		filepath.Join(t.TempDir(), "unrelated.txt"),
	} {
		if err := g.Check(target); err != nil {
			t.Errorf("Check(%q) = %v, want nil", target, err)
		}
	}
}

func TestSessionDataGuardZeroValueUnconfined(t *testing.T) {
	var g SessionDataGuard
	if err := g.Check("/anywhere/sessions/x.jsonl"); err != nil {
		t.Errorf("zero-value guard should be unconfined, got %v", err)
	}
	if hint := g.CommandHint("rm -rf ~/.reasonix/sessions"); hint != "" {
		t.Errorf("zero-value guard hint = %q, want empty", hint)
	}
}

func TestSessionDataGuardAllowWriteEscapeHatch(t *testing.T) {
	root, cliSession, projectSession := stateRootFor(t)
	g := NewSessionDataGuard(root, []string{filepath.Join(root, "sessions")})

	if err := g.Check(cliSession); err != nil {
		t.Errorf("allow_write-listed store should pass, got %v", err)
	}
	// The other store stays guarded.
	if err := g.Check(projectSession); err == nil {
		t.Error("project store should stay denied when only the CLI store is allowed")
	}
}

func TestWriteToolsRejectSessionData(t *testing.T) {
	root, cliSession, projectSession := stateRootFor(t)
	// Workspace root covers the state root — the accidental self-write shape
	// (e.g. a home-directory workspace).
	guard := NewSessionDataGuard(root, nil)
	tools := ConfineWriters([]string{root}, guard)

	argsFor := func(name, target string) json.RawMessage {
		var m map[string]any
		switch name {
		case "write_file":
			m = map[string]any{"path": target, "content": "tampered"}
		case "edit_file":
			m = map[string]any{"path": target, "old_string": "{}", "new_string": "[]"}
		case "multi_edit":
			m = map[string]any{"path": target, "edits": []map[string]any{{"old_string": "{}", "new_string": "[]"}}}
		case "move_file":
			m = map[string]any{"source_path": target, "destination_path": target + ".bak"}
		case "notebook_edit":
			m = map[string]any{"path": target, "cell_index": 0, "mode": "delete"}
		case "delete_range":
			m = map[string]any{"path": target, "start_anchor": "{}", "end_anchor": "{}"}
		case "delete_symbol":
			m = map[string]any{"path": target, "name": "x"}
		default:
			t.Fatalf("unhandled tool %s", name)
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	for _, tl := range tools {
		for _, target := range []string{cliSession, projectSession} {
			_, err := tl.Execute(context.Background(), argsFor(tl.Name(), target))
			if err == nil || !strings.Contains(err.Error(), "session data") {
				t.Errorf("%s on %q: err = %v, want session-data denial", tl.Name(), target, err)
			}
		}
		// The same tool still writes ordinary workspace files (guard is not a
		// blanket block on the state root).
		if tl.Name() == "write_file" {
			ok := filepath.Join(root, "notes.txt")
			if _, err := tl.Execute(context.Background(), argsFor("write_file", ok)); err != nil {
				t.Errorf("write_file on ordinary path: %v", err)
			}
		}
	}
}

func TestSessionDataGuardCommandHint(t *testing.T) {
	root, cliSession, _ := stateRootFor(t)
	g := NewSessionDataGuard(root, nil)

	hinted := []string{
		"python3 fix.py " + cliSession,
		"rm -rf " + filepath.ToSlash(filepath.Join(root, "projects", "slug", "sessions")),
		"Get-Content " + strings.ToUpper(filepath.ToSlash(filepath.Join(root, "sessions"))) + "/x.jsonl", // case-insensitive
	}
	for _, cmd := range hinted {
		if hint := g.CommandHint(cmd); hint == "" {
			t.Errorf("CommandHint(%q) = empty, want warning", cmd)
		} else if !strings.Contains(hint, "conflict cop") {
			t.Errorf("CommandHint(%q) = %q, want conflict-copy explanation", cmd, hint)
		}
	}
	for _, cmd := range []string{
		"go test ./...",
		"ls " + filepath.Join(t.TempDir(), "sessions"), // "sessions" under an unrelated root
		"",
	} {
		if hint := g.CommandHint(cmd); hint != "" {
			t.Errorf("CommandHint(%q) = %q, want empty", cmd, hint)
		}
	}
}

func TestBashAppendsSessionDataHint(t *testing.T) {
	root, cliSession, _ := stateRootFor(t)
	guard := NewSessionDataGuard(root, nil)
	b := ConfineBash(sandbox.Spec{Mode: "off"}, guard)

	args, _ := json.Marshal(map[string]string{"command": "echo " + cliSession})
	out, err := b.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "WARNING: this command referenced Reasonix's own session/state data") {
		t.Fatalf("bash output missing session-data warning:\n%s", out)
	}
	// An ordinary command stays clean.
	args, _ = json.Marshal(map[string]string{"command": "echo hello"})
	out, err = b.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if strings.Contains(out, "WARNING") {
		t.Fatalf("bash output has spurious warning:\n%s", out)
	}
}
