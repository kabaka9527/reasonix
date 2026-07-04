//go:build windows

package sandbox

import (
	"os"
	"strings"
	"testing"

	winsandbox "github.com/SivanCola/windows-sandbox"
)

func TestWindowsCommandWrapsWithHelper(t *testing.T) {
	cmd, wrapped := Command(Spec{Mode: "enforce", WriteRoots: []string{`C:\work`}, Network: true}, Shell{Kind: ShellPowerShell, Path: "powershell"}, "Write-Output ok")
	if !wrapped {
		t.Fatal("windows enforce should wrap through helper")
	}
	if len(cmd) < 6 {
		t.Fatalf("wrapped argv too short: %v", cmd)
	}
	if got := cmd[1]; got != WindowsHelperCommand {
		t.Fatalf("helper command = %q, want %q (argv=%v)", got, WindowsHelperCommand, cmd)
	}
	if cmd[3] != "--" {
		t.Fatalf("helper argv separator = %q, want -- (argv=%v)", cmd[3], cmd)
	}
	payload, err := decodeWindowsSandboxPayload(cmd[2])
	if err != nil {
		t.Fatalf("decode helper payload: %v", err)
	}
	if payload.Spec.Mode != "enforce" || !payload.Spec.Network || len(payload.Spec.WriteRoots) != 1 || !payload.Writable {
		t.Fatalf("payload not preserved: %+v writable=%v", payload.Spec, payload.Writable)
	}
	if !strings.Contains(strings.Join(cmd[4:], " "), "Write-Output ok") {
		t.Fatalf("child argv not appended: %v", cmd)
	}
}

func TestWindowsCommandArgsWrapsReadOnly(t *testing.T) {
	cmd, wrapped := CommandArgs(Spec{Mode: "enforce", WriteRoots: []string{`C:\work`}, Network: false}, []string{`C:\tools\rg.exe`, "needle"})
	if !wrapped {
		t.Fatal("windows enforce should wrap direct argv through helper")
	}
	payload, err := decodeWindowsSandboxPayload(cmd[2])
	if err != nil {
		t.Fatalf("decode helper payload: %v", err)
	}
	if payload.Writable {
		t.Fatalf("direct argv should be marked read-only: %+v", payload)
	}
	if payload.Spec.Network {
		t.Fatalf("network=false should be preserved for AppContainer launch: %+v", payload.Spec)
	}
}

func TestConvertWindowsSandboxSpec(t *testing.T) {
	spec := Spec{
		Mode:            "enforce",
		WriteRoots:      []string{`C:\work`},
		ForbidReadRoots: []string{`C:\work\secret`},
		Network:         true,
	}
	got := convertWindowsSandboxSpec(spec, true)
	if !got.Writable || !got.Network || got.TempPrefix != "reasonix-sandbox-" {
		t.Fatalf("converted flags = %+v", got)
	}
	if len(got.WritableRoots) != 1 || got.WritableRoots[0] != spec.WriteRoots[0] {
		t.Fatalf("converted writable roots = %v", got.WritableRoots)
	}
	if len(got.ForbidReadRoots) != 1 || got.ForbidReadRoots[0] != spec.ForbidReadRoots[0] {
		t.Fatalf("converted forbid roots = %v", got.ForbidReadRoots)
	}

	got.WritableRoots[0] = `C:\mutated`
	got.ForbidReadRoots[0] = `C:\mutated`
	if spec.WriteRoots[0] == got.WritableRoots[0] || spec.ForbidReadRoots[0] == got.ForbidReadRoots[0] {
		t.Fatal("converted spec should not alias Reasonix slices")
	}
}

func TestWindowsSandboxAvailableOnCI(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("only require Windows sandbox availability on CI")
	}
	if !Available() {
		t.Fatal("windows sandbox APIs unavailable on CI")
	}
	if !winsandbox.Available() {
		t.Fatal("external windows sandbox APIs unavailable on CI")
	}
}
