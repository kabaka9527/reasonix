package agent

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSessionLeaseRejectsConcurrentWriterAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first, err := TryAcquireSessionLease(path)
	if err != nil {
		t.Fatalf("first TryAcquireSessionLease: %v", err)
	}
	if first.Path() == "" {
		t.Fatal("first lease path is empty")
	}
	info, err := LoadSessionLeaseInfo(path)
	if err != nil {
		t.Fatalf("LoadSessionLeaseInfo: %v", err)
	}
	if info.WriterID == "" || info.PID == 0 || info.SessionPath == "" {
		t.Fatalf("lease info = %+v, want writer metadata", info)
	}

	second, err := TryAcquireSessionLease(path)
	if !errors.Is(err, ErrSessionLeaseHeld) {
		t.Fatalf("second TryAcquireSessionLease err = %v, want ErrSessionLeaseHeld", err)
	}
	if second != nil {
		second.Release()
		t.Fatal("second lease unexpectedly acquired")
	}

	first.Release()
	third, err := TryAcquireSessionLease(path)
	if err != nil {
		t.Fatalf("third TryAcquireSessionLease after release: %v", err)
	}
	third.Release()
}
