package environment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFormatSectionSortsAndRedacts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	section := FormatSection([]ProbeResult{
		{Binary: "python3", Found: true, Output: "Python 3.12.0"},
		{Binary: "go", Found: true, Output: "go version go1.24 darwin/arm64"},
		{Binary: "docker", Error: "not found"},
	}, "darwin/arm64", filepath.Join(home, "bin", "bash"), map[string]string{
		"python3": filepath.Join(home, ".pyenv", "shims", "python3"),
		"go":      "/opt/homebrew/bin/go",
	})

	for _, want := range []string{
		"## Environment",
		"- OS: darwin/arm64",
		"- Shell: ~/bin/bash",
		"Configured tools:\n- go: /opt/homebrew/bin/go\n- python3: ~/.pyenv/shims/python3",
		"Detected tools:\n- go: go version go1.24 darwin/arm64\n- python3: Python 3.12.0",
		"Not found or unavailable:\n- docker: not found",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q:\n%s", want, section)
		}
	}
}

func TestRunProbesReportsMissingCommand(t *testing.T) {
	results := RunProbes(context.Background(), []string{"__reasonix_missing_probe__ --version"})
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Found {
		t.Fatalf("missing command marked found: %+v", results[0])
	}
	if results[0].Error != "not found" {
		t.Fatalf("Error = %q, want not found", results[0].Error)
	}
}

func TestRunProbesUsesOverridePathAndFirstLine(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "mytool")
	body := "#!/bin/sh\nprintf 'custom version\\nignored\\n'\n"
	if runtime.GOOS == "windows" {
		toolPath += ".bat"
		body = "@echo custom version\r\n@echo ignored\r\n"
	}
	if err := os.WriteFile(toolPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	results := RunProbesWithOverrides(context.Background(), []string{"mytool --version"}, map[string]string{"mytool": toolPath})
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if !results[0].Found {
		t.Fatalf("override command not found: %+v", results[0])
	}
	if results[0].Output != "custom version" {
		t.Fatalf("Output = %q, want first line", results[0].Output)
	}
}

func TestRunProbesReportsTimeout(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "slowtool")
	body := "#!/bin/sh\nsleep 3\n"
	if runtime.GOOS == "windows" {
		toolPath += ".bat"
		body = "@ping 127.0.0.1 -n 4 > nul\r\n"
	}
	if err := os.WriteFile(toolPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	results := RunProbesWithOverrides(context.Background(), []string{"slowtool --version"}, map[string]string{"slowtool": toolPath})
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Found {
		t.Fatalf("timeout command marked found: %+v", results[0])
	}
	if results[0].Error != "timeout" {
		t.Fatalf("Error = %q, want timeout", results[0].Error)
	}
}

func TestFormatSectionLimitsToolOutput(t *testing.T) {
	overrides := map[string]string{}
	var results []ProbeResult
	for i := 0; i < maxRenderedTools+2; i++ {
		name := fmt.Sprintf("tool%02d", i)
		overrides[name] = "/bin/" + name
		results = append(results, ProbeResult{Binary: name, Found: true, Output: "ok"})
		results = append(results, ProbeResult{Binary: "missing" + name, Error: "not found"})
	}

	section := FormatSection(results, "test/os", "", overrides)
	for _, want := range []string{
		"- ... 2 more configured tools omitted",
		"- ... 2 more detected tools omitted",
		"- ... 2 more unavailable tools omitted",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q:\n%s", want, section)
		}
	}
}
