package tool_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
	_ "reasonix/internal/tool/builtin"
)

func TestBuiltinToolContractDocumentation(t *testing.T) {
	entries := tool.BuiltinContractEntries()
	if len(entries) == 0 {
		t.Fatal("no built-in tool contract entries")
	}
	doc, err := os.ReadFile("../../docs/TOOL_CONTRACT.md")
	if err != nil {
		t.Fatalf("read docs/TOOL_CONTRACT.md: %v", err)
	}
	text := string(doc)
	for _, e := range entries {
		if !strings.Contains(text, "| `"+e.Name+"` |") {
			t.Errorf("documentation missing table row for %s", e.Name)
		}
		if !strings.Contains(text, "| `"+e.Name+"` | "+boolString(e.ReadOnly)+" |") {
			t.Errorf("documentation missing read-only flag for %s", e.Name)
		}
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("%s has empty description", e.Name)
		}
		if !json.Valid(e.Schema) {
			t.Errorf("%s schema is invalid JSON: %s", e.Name, e.Schema)
		}
		if got := string(provider.CanonicalizeSchema(e.Schema)); got != string(e.Schema) {
			t.Errorf("%s schema is not canonical", e.Name)
		}
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
