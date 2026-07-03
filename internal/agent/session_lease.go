package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/fileutil"
)

var ErrSessionLeaseHeld = errors.New("session lease held by another runtime")

var sessionLeaseOwners sync.Map

type SessionLeaseInfo struct {
	SessionPath string    `json:"session_path"`
	WriterID    string    `json:"writer_id"`
	PID         int       `json:"pid"`
	Hostname    string    `json:"hostname,omitempty"`
	AcquiredAt  time.Time `json:"acquired_at"`
}

type SessionLeaseError struct {
	Path string
	Info *SessionLeaseInfo
}

func (e *SessionLeaseError) Error() string {
	if e == nil {
		return ErrSessionLeaseHeld.Error()
	}
	if e.Info != nil && e.Info.WriterID != "" {
		return fmt.Sprintf("%s: %s is held by %s", ErrSessionLeaseHeld, e.Path, e.Info.WriterID)
	}
	return fmt.Sprintf("%s: %s", ErrSessionLeaseHeld, e.Path)
}

func (e *SessionLeaseError) Unwrap() error {
	return ErrSessionLeaseHeld
}

type SessionLease struct {
	path   string
	unlock func()
	once   sync.Once
}

func TryAcquireSessionLease(path string) (*SessionLease, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("empty session path")
	}
	path = canonicalSessionSavePath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, loaded := sessionLeaseOwners.LoadOrStore(path, struct{}{}); loaded {
		info, _ := LoadSessionLeaseInfo(path)
		return nil, &SessionLeaseError{Path: path, Info: info}
	}
	unlock, err := tryLockSessionLeaseFile(path)
	if err != nil {
		sessionLeaseOwners.Delete(path)
		if errors.Is(err, ErrSessionLeaseHeld) {
			info, _ := LoadSessionLeaseInfo(path)
			return nil, &SessionLeaseError{Path: path, Info: info}
		}
		return nil, err
	}
	lease := &SessionLease{path: path, unlock: unlock}
	if err := SaveSessionLeaseInfo(path, newSessionLeaseInfo(path)); err != nil {
		lease.Release()
		return nil, err
	}
	return lease, nil
}

func (l *SessionLease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *SessionLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = os.Remove(sessionLeaseInfoPath(l.path))
		if l.unlock != nil {
			l.unlock()
		}
		sessionLeaseOwners.Delete(l.path)
	})
}

func newSessionLeaseInfo(path string) SessionLeaseInfo {
	host, _ := os.Hostname()
	return SessionLeaseInfo{
		SessionPath: path,
		WriterID:    SessionWriterID(),
		PID:         os.Getpid(),
		Hostname:    host,
		AcquiredAt:  time.Now().UTC(),
	}
}

func LoadSessionLeaseInfo(path string) (*SessionLeaseInfo, error) {
	b, err := os.ReadFile(sessionLeaseInfoPath(path))
	if err != nil {
		return nil, err
	}
	var info SessionLeaseInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func SaveSessionLeaseInfo(path string, info SessionLeaseInfo) error {
	leasePath := sessionLeaseInfoPath(path)
	if err := os.MkdirAll(filepath.Dir(leasePath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(leasePath), ".lease.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := fileutil.ReplaceFile(tmpPath, leasePath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func sessionLeaseInfoPath(path string) string {
	return canonicalSessionSavePath(path) + ".lease.json"
}
