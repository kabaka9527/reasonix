package main

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"reasonix/internal/plugin"
)

// sharedPluginHost is a reference-counted plugin.Host shared across tabs
// that share the same workspace root. Multiple controllers (one per tab)
// use the same Host so MCP subprocesses (CodeGraph, etc.) are spawned once.
type sharedPluginHost struct {
	host *plugin.Host
	refs int
}

// acquireSharedHost returns a shared *plugin.Host for the given workspace root.
// The first call creates the host; subsequent calls increment a refcount and
// return the same host. The caller must call releaseSharedHost when the tab
// no longer needs the host.
func (a *App) acquireSharedHost(root string) *plugin.Host {
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()

	if a.sharedHosts == nil {
		a.sharedHosts = make(map[string]*sharedPluginHost)
	}

	entry, ok := a.sharedHosts[root]
	if ok {
		entry.refs++
		slog.Debug("shared host acquired (reused)", "root", root, "refs", entry.refs)
		return entry.host
	}

	host := plugin.NewHost()
	a.sharedHosts[root] = &sharedPluginHost{host: host, refs: 1}
	slog.Debug("shared host acquired (new)", "root", root)
	return host
}

// lookupSharedHost returns an existing shared host for the given root, or nil.
// Unlike acquireSharedHost, it does NOT increment the refcount — use this when
// rebuilding a controller for an existing tab that already holds a reference.
func (a *App) lookupSharedHost(root string) *plugin.Host {
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()
	if a.sharedHosts == nil {
		return nil
	}
	entry, ok := a.sharedHosts[root]
	if !ok {
		return nil
	}
	return entry.host
}

// reapOrphanCodeGraph kills any codegraph MCP subprocess that is not a
// direct child of the current Reasonix process. This cleans up orphaned
// processes from a previous crash or from older versions that leaked them,
// preventing accumulation across restarts.
func (a *App) reapOrphanCodeGraph() {
	myPID := os.Getpid()

	// Collect the PIDs of our direct children (the ones we own).
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(myPID)).Output()
	if err != nil {
		return // no children or pgrep unavailable
	}
	ours := map[int]bool{}
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			ours[pid] = true
		}
	}

	// Find every codegraph MCP process.
	out, err = exec.Command("pgrep", "-f", "codegraph.js serve --mcp").Output()
	if err != nil {
		return
	}
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid == myPID || ours[pid] {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
			slog.Debug("reaped orphan codegraph", "pid", pid)
		}
	}
}

// releaseSharedHost decrements the refcount for the workspace root and closes
// the shared host when no tabs reference it any more. Safe to call even when
// no acquire was made (no-op).
func (a *App) releaseSharedHost(root string) {
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()

	entry, ok := a.sharedHosts[root]
	if !ok {
		return
	}
	entry.refs--
	if entry.refs > 0 {
		slog.Debug("shared host released (still in use)", "root", root, "refs", entry.refs)
		return
	}

	delete(a.sharedHosts, root)
	entry.host.Close()
	slog.Debug("shared host closed", "root", root)
}

// closeAllSharedHosts closes every shared host. Called during app shutdown.
func (a *App) closeAllSharedHosts() {
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()

	for root, entry := range a.sharedHosts {
		delete(a.sharedHosts, root)
		entry.host.Close()
		slog.Debug("shared host closed (shutdown)", "root", root)
	}
}
