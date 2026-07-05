package acp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// TestACPRebuildSessionContinuesRecoveryPathAfterSnapshotConflict is the ACP
// twin of the desktop rebuild fix: when the pre-rebuild Snapshot hits a
// conflict and retargets the old controller to a recovery branch, AdoptHistory
// must bind the replacement controller to that recovery path. A pre-snapshot
// capture bound the just-recovered transcript back to the original file, so
// every later save re-conflicted and derived yet another recovery branch.
func TestACPRebuildSessionContinuesRecoveryPathAfterSnapshotConflict(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "acp-switch-conflict.jsonl")

	disk := agent.NewSession("sys prompt")
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	disk.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "disk second"})
	if err := disk.Save(originalPath); err != nil {
		t.Fatalf("save disk session: %v", err)
	}

	stale := agent.NewSession("sys prompt")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	stale.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "local second"})

	sink := newUpdateSink(&fakeNotifier{}, "sess-recovery")
	sess := &acpSession{
		id:    "sess-recovery",
		sink:  sink,
		cwd:   dir,
		model: "fast",
	}
	oldCtrl := control.New(control.Options{
		Executor:    agent.New(nil, nil, stale, agent.Options{}, event.Discard),
		SessionDir:  dir,
		SessionPath: originalPath,
		Label:       "fast",
	})
	sess.ctrl = oldCtrl
	svc := &service{
		factory:  &configurableFactory{dir: dir},
		sessions: map[string]*acpSession{sess.id: sess},
	}

	if err := svc.rebuildSession(context.Background(), sess, SessionConfigState{Model: "pro"}); err != nil {
		t.Fatalf("rebuildSession: %v", err)
	}
	if sess.ctrl == oldCtrl {
		t.Fatal("session controller was not replaced")
	}

	recoveryPath := sess.ctrl.SessionPath()
	if recoveryPath == "" || recoveryPath == originalPath || !strings.Contains(filepath.Base(recoveryPath), "-recovery-") {
		t.Fatalf("rebuilt session path = %q, want recovery path distinct from %q", recoveryPath, originalPath)
	}

	// The rebuilt controller adopted the recovery file's baseline, so its next
	// snapshot must not derive a second recovery branch.
	if err := sess.ctrl.Snapshot(); err != nil {
		t.Fatalf("Snapshot after rebuild: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*-recovery-*.jsonl"))
	if err != nil {
		t.Fatalf("glob recovery branches: %v", err)
	}
	primary := matches[:0]
	for _, path := range matches {
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".events.jsonl") || strings.HasSuffix(base, ".guardian.jsonl") {
			continue
		}
		primary = append(primary, path)
	}
	if len(primary) != 1 || primary[0] != recoveryPath {
		t.Fatalf("recovery branches after follow-up snapshot = %v, want only %q", primary, recoveryPath)
	}
}
