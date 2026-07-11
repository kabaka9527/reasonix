package skill

import (
	"fmt"
	"os"
	"path/filepath"

	"reasonix/internal/frontmatter"
)

// subagentProfileManagedKeys are the frontmatter keys the desktop and CLI
// profile editors can round-trip without changing execution semantics.
var subagentProfileManagedKeys = map[string]bool{
	"name": true, "description": true, "color": true, "invocation": true,
	"runas": true, "model": true, "effort": true, "allowed-tools": true,
}

// ValidateEditableSubagentProfile verifies that a loaded skill is a manual
// subagent profile whose backing file can be losslessly rewritten by the
// profile editors. Rich hand-authored skills remain owned by the Skills
// workflow so read-only, routing, references, and scripts are never dropped.
func ValidateEditableSubagentProfile(sk Skill) error {
	if sk.RunAs != RunSubagent {
		return fmt.Errorf("%q is not a subagent profile (runAs is not \"subagent\") — manage it as a skill file instead", sk.Name)
	}
	if sk.Invocation != "manual" {
		return fmt.Errorf("%q was not created by a subagent profile editor (invocation is not \"manual\") — manage it as a skill file instead", sk.Name)
	}
	if sk.Scope != ScopeProject && sk.Scope != ScopeGlobal {
		return fmt.Errorf("%q is scope %q and cannot be edited as a project/global subagent profile", sk.Name, sk.Scope)
	}
	if sk.Path == "" || sk.Path == "(builtin)" {
		return fmt.Errorf("%q has no editable file", sk.Name)
	}
	raw, err := os.ReadFile(sk.Path)
	if err != nil {
		return err
	}
	fm, _ := frontmatter.Split(string(raw))
	for key := range fm {
		if !subagentProfileManagedKeys[key] {
			return fmt.Errorf("%q carries frontmatter this editor does not manage (%s) and would silently drop — edit it as a skill file instead", sk.Name, key)
		}
	}
	if filepath.Base(sk.Path) == SkillFile {
		dir := filepath.Dir(sk.Path)
		for _, sibling := range []string{"references", "scripts"} {
			if info, err := os.Stat(filepath.Join(dir, sibling)); err == nil && info.IsDir() {
				return fmt.Errorf("%q has a %s/ directory whose content is folded into the body at load time — editing here would bake it into the main file; edit it as a skill file instead", sk.Name, sibling)
			}
		}
	}
	return nil
}
