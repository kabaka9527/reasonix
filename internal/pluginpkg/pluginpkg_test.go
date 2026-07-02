package pluginpkg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodexSuperpowersManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, CodexManifest), `{
	  "name": "superpowers",
	  "version": "6.1.0",
	  "description": "Planning workflows",
	  "skills": "./skills/"
	}`)
	writeTestFile(t, filepath.Join(root, "hooks", "session-start-codex"), "#!/usr/bin/env bash\n")

	pkg, warnings, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if pkg.ManifestKind != "codex" || pkg.Manifest.Name != "superpowers" || pkg.Manifest.Version != "6.1.0" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if got := pkg.SkillRoots(); len(got) != 1 || got[0] != filepath.Join(root, "skills") {
		t.Fatalf("SkillRoots = %#v", got)
	}
	if hooks := pkg.Manifest.Hooks["SessionStart"]; len(hooks) != 1 || hooks[0].Command != filepath.Join(root, "hooks", "session-start-codex") {
		t.Fatalf("SessionStart hooks = %+v", hooks)
	}
}

func TestRejectsEscapingSkillPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, NativeManifest), `{
	  "name": "bad",
	  "skills": "../skills"
	}`)
	if _, _, err := ParseDir(root); err == nil {
		t.Fatal("ParseDir should reject escaping skill path")
	}
}

func TestStateRoundTripSortsPlugins(t *testing.T) {
	home := t.TempDir()
	if err := Upsert(home, InstalledPlugin{Name: "zeta", Root: "plugins/zeta", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(home, InstalledPlugin{Name: "alpha", Root: "plugins/alpha", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Plugins) != 2 || st.Plugins[0].Name != "alpha" || st.Plugins[1].Name != "zeta" {
		t.Fatalf("state plugins = %+v", st.Plugins)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
