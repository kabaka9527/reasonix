package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionDataGuard rejects agent writes into Reasonix's own session stores:
// <state root>/sessions and <state root>/projects/<slug>/sessions. The runtime
// is the only writer of those files (CAS ledger + autosave); an agent editing
// them from inside a chat races the app's own saves, which surfaces to the user
// as endless "conflict copy" forks — the agent sees its write "not take",
// retries, and loops. The zero value is unconfined, matching the confine
// helpers, so tools registered at init keep their historical behavior.
//
// allowRoots are the explicitly configured [sandbox] allow_write entries: a
// user who deliberately lists a session directory there keeps raw access, so
// the guard only blocks the accidental self-write path (a workspace root that
// happens to cover the state root, e.g. a home-directory workspace).
type SessionDataGuard struct {
	stateRoot   string
	allowRoots  []string
	hintNeedles []string
}

// NewSessionDataGuard builds a guard for the given Reasonix state root
// (config.MemoryUserDir()) and the explicit allow_write entries. Both are
// resolved to absolute, symlink-free paths once here, mirroring realRoots.
// An empty stateRoot yields an unconfined guard.
func NewSessionDataGuard(stateRoot string, allowRoots []string) SessionDataGuard {
	g := SessionDataGuard{}
	if strings.TrimSpace(stateRoot) == "" {
		return g
	}
	real, err := realPath(stateRoot)
	if err != nil {
		return g
	}
	g.stateRoot = real
	g.allowRoots = realRoots(allowRoots)
	g.hintNeedles = sessionHintNeedles(stateRoot, real, g.allowRoots)
	return g
}

// Check returns an error when target resolves into a guarded session store and
// is not covered by an explicit allow_write root. The error text is written for
// the model: it names why the write is refused and the durable ways forward.
func (g SessionDataGuard) Check(target string) error {
	if g.stateRoot == "" {
		return nil
	}
	abs, err := realPath(target)
	if err != nil {
		return nil // can't resolve -> let the caller's normal error path handle it
	}
	if !g.denies(abs) {
		return nil
	}
	return fmt.Errorf("path %q is inside Reasonix's own session data (%s); the app is the only writer of these files, and edits from a chat race its saves — that surfaces as repeated save-conflict copies. "+
		"Do not modify session files directly; report the underlying problem instead. If raw access is truly intended, add the directory to [sandbox] allow_write in reasonix.toml",
		target, g.stateRoot)
}

// denies reports whether abs (absolute, symlink-free) is inside a guarded
// session store and not explicitly allowed.
func (g SessionDataGuard) denies(abs string) bool {
	for _, a := range g.allowRoots {
		if within(a, abs) {
			return false
		}
	}
	if within(filepath.Join(g.stateRoot, "sessions"), abs) {
		return true
	}
	// <state root>/projects/<slug>/sessions/** — every per-project store, so
	// the slug segment is matched positionally rather than enumerated.
	projects := filepath.Join(g.stateRoot, "projects")
	if !within(projects, abs) {
		return false
	}
	rel, err := filepath.Rel(projects, abs)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) >= 2 && parts[1] == "sessions"
}

// CommandHint returns a warning to append to bash output when the command text
// references the guarded state trees, and "" otherwise. bash cannot know what a
// command actually wrote (off mode runs raw, and write roots may legitimately
// cover the state root), so this is a lexical check on the command text —
// enough to break the agent's "write → app overwrites it → looks like my write
// failed → retry" loop, which is how session-data self-writes burn tokens in
// the wild. It never blocks: reading session files for diagnostics is
// legitimate.
func (g SessionDataGuard) CommandHint(command string) string {
	if g.stateRoot == "" || command == "" {
		return ""
	}
	haystack := strings.ToLower(filepath.ToSlash(command))
	for _, needle := range g.hintNeedles {
		if strings.Contains(haystack, needle) {
			return fmt.Sprintf("WARNING: this command referenced Reasonix's own session/state data under %s. "+
				"The app is actively saving those files; external modifications conflict with its saves and are preserved as conflict copies, so an edit can look like it \"did not take\". "+
				"Do not modify session files from a chat — stop retrying and report the underlying problem instead.", g.stateRoot)
		}
	}
	return ""
}

// sessionHintNeedles precomputes the lowercase, slash-normalized textual forms
// of the guarded trees as they may appear in a command: under the state root as
// given, its symlink-resolved form, and "~/"-abbreviated variants when a form
// sits under the user's home. A tree wholly covered by an allow_write root is
// skipped — the user sanctioned raw access there, so warnings would only nag.
func sessionHintNeedles(rawRoot, realRoot string, allowRoots []string) []string {
	prefixes := map[string]bool{}
	addPrefix := func(p string) {
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			prefixes[filepath.Clean(abs)] = true
		}
	}
	addPrefix(rawRoot)
	addPrefix(realRoot)
	home, _ := os.UserHomeDir()

	var needles []string
	for prefix := range prefixes {
		for _, sub := range []string{"sessions", "projects"} {
			tree := filepath.Join(prefix, sub)
			if covered := func() bool {
				for _, a := range allowRoots {
					if within(a, filepath.Join(realRoot, sub)) {
						return true
					}
				}
				return false
			}(); covered {
				continue
			}
			needles = append(needles, strings.ToLower(filepath.ToSlash(tree)))
			if home != "" {
				if rel, err := filepath.Rel(home, tree); err == nil && rel != "." && filepath.IsLocal(rel) {
					needles = append(needles, strings.ToLower(filepath.ToSlash(filepath.Join("~", rel))))
				}
			}
		}
	}
	return needles
}
