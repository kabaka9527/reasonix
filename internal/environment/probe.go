// Package environment probes the local developer environment at startup and
// renders a small, stable model-facing summary.
package environment

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const ProbeTimeout = 2 * time.Second

const maxRenderedTools = 24

type ProbeResult struct {
	Command string
	Binary  string
	Output  string
	Found   bool
	Error   string
}

func DefaultProbes() []string {
	return []string{
		"go version",
		"python3 --version",
		"python --version",
		"node --version",
		"npm --version",
		"rustc --version",
		"cargo --version",
		"git version",
		"make --version",
		"rg --version",
		"docker --version",
	}
}

func RunProbes(ctx context.Context, commands []string) []ProbeResult {
	return RunProbesWithOverrides(ctx, commands, nil)
}

func RunProbesWithOverrides(ctx context.Context, commands []string, overrides map[string]string) []ProbeResult {
	results := make([]ProbeResult, len(commands))
	var wg sync.WaitGroup
	for i, command := range commands {
		wg.Add(1)
		go func(i int, command string) {
			defer wg.Done()
			results[i] = runOne(ctx, command, overrides)
		}(i, command)
	}
	wg.Wait()
	sortResults(results)
	return results
}

func runOne(ctx context.Context, command string, overrides map[string]string) ProbeResult {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ProbeResult{Command: command, Binary: command, Error: "empty command"}
	}
	res := ProbeResult{Command: command, Binary: parts[0]}
	exe := parts[0]
	if override := strings.TrimSpace(overrides[parts[0]]); override != "" {
		exe = expandHome(override)
		if !fileExecutable(exe) {
			res.Error = "not found"
			return res
		}
	} else {
		found, err := exec.LookPath(parts[0])
		if err != nil {
			res.Error = "not found"
			return res
		}
		exe = found
	}
	cmdCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, exe, parts[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			res.Error = "timeout"
			return res
		}
		if out == "" {
			res.Error = "exit " + err.Error()
			return res
		}
		res.Error = firstLine(out)
		return res
	}
	res.Found = true
	res.Output = firstLine(out)
	return res
}

func sortResults(results []ProbeResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Found != results[j].Found {
			return results[i].Found
		}
		return results[i].Binary < results[j].Binary
	})
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], "\r")
	}
	return s
}

func FormatSection(results []ProbeResult, osName, shellPath string, overrides map[string]string) string {
	if len(results) == 0 && len(overrides) == 0 && osName == "" && shellPath == "" {
		return ""
	}
	results = append([]ProbeResult(nil), results...)
	sortResults(results)
	var b strings.Builder
	b.WriteString("## Environment\n\n")
	if osName == "" {
		osName = runtime.GOOS + "/" + runtime.GOARCH
	}
	b.WriteString("- OS: " + osName + "\n")
	if shellPath != "" {
		b.WriteString("- Shell: " + redactHome(shellPath) + "\n")
	}
	if len(overrides) > 0 {
		b.WriteString("\nConfigured tools:\n")
		names := sortedMapKeys(overrides)
		for _, name := range limitStrings(names, maxRenderedTools) {
			b.WriteString(fmt.Sprintf("- %s: %s\n", name, redactHome(overrides[name])))
		}
		if omitted := len(names) - maxRenderedTools; omitted > 0 {
			b.WriteString(fmt.Sprintf("- ... %d more configured tools omitted\n", omitted))
		}
	}
	if len(results) > 0 {
		b.WriteString("\nDetected tools:\n")
		foundShown := 0
		foundTotal := 0
		for _, r := range results {
			if r.Found {
				foundTotal++
				if foundShown >= maxRenderedTools {
					continue
				}
				out := r.Output
				if out == "" {
					out = "available"
				}
				b.WriteString(fmt.Sprintf("- %s: %s\n", r.Binary, out))
				foundShown++
			}
		}
		if omitted := foundTotal - foundShown; omitted > 0 {
			b.WriteString(fmt.Sprintf("- ... %d more detected tools omitted\n", omitted))
		}
		b.WriteString("\nNot found or unavailable:\n")
		missingShown := 0
		missingTotal := 0
		for _, r := range results {
			if !r.Found {
				missingTotal++
				if missingShown >= maxRenderedTools {
					continue
				}
				reason := r.Error
				if reason == "" {
					reason = "not found"
				}
				b.WriteString(fmt.Sprintf("- %s: %s\n", r.Binary, reason))
				missingShown++
			}
		}
		if omitted := missingTotal - missingShown; omitted > 0 {
			b.WriteString(fmt.Sprintf("- ... %d more unavailable tools omitted\n", omitted))
		}
	}
	b.WriteString("\nUse detected tools when appropriate. Do not try unavailable tools unless the user installs or configures them.\n")
	return strings.TrimRight(b.String(), "\n")
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func limitStrings(in []string, limit int) []string {
	if len(in) <= limit {
		return in
	}
	return in[:limit]
}

func redactHome(path string) string {
	path = expandHome(path)
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Clean(path)
	}
	clean := filepath.Clean(path)
	home = filepath.Clean(home)
	if clean == home {
		return "~"
	}
	if strings.HasPrefix(clean, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(clean, home)
	}
	return clean
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "~" || !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

func fileExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
