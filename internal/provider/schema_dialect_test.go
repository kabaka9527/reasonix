package provider

import (
	"encoding/json"
	"testing"
)

func TestIsMiMoEndpoint(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://api.xiaomimimo.com/v1", true},
		{"https://api.xiaomimimo.com/anthropic", true},
		{"https://token-plan-cn.xiaomimimo.com/v1", true},
		{"https://token-plan-sgp.xiaomimimo.com/anthropic", true},
		{"https://token-plan-ams.xiaomimimo.com/v1", true},
		{"https://xiaomimimo.com/v1", false},
		{"https://api.deepseek.com", false},
		{"https://xiaomimimo.com.example.org", false},
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsMiMoEndpoint(tc.url); got != tc.want {
			t.Errorf("IsMiMoEndpoint(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestNormalizeLegacyTupleItemsDoesNotRewriteSchemaExamples(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"value":{
				"type":"object",
				"default":{"items":["first","second"]}
			}
		}
	}`)
	got := NormalizeLegacyTupleItemsForDraft202012(raw)
	var schema map[string]any
	if err := json.Unmarshal(got, &schema); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	value := properties["value"].(map[string]any)
	defaultValue := value["default"].(map[string]any)
	if _, ok := defaultValue["items"].([]any); !ok {
		t.Fatalf("schema default was rewritten: %s", got)
	}
	if _, exists := defaultValue["prefixItems"]; exists {
		t.Fatalf("schema default gained prefixItems: %s", got)
	}
}

func TestNormalizeLegacyTupleItemsUpdatesRootDialect(t *testing.T) {
	raw := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"array","items":[{"type":"string"},{"type":"number"}]}`)
	var schema map[string]any
	if err := json.Unmarshal(NormalizeLegacyTupleItemsForDraft202012(raw), &schema); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := schema["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %v, want 2020-12 after tuple conversion (old declaration would misread prefixItems)", got)
	}
	if _, ok := schema["prefixItems"].([]any); !ok {
		t.Fatalf("prefixItems missing: %v", schema)
	}
}

func TestNormalizeLegacyTupleItemsUpdatesNestedResourceDialect(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","$defs":{"pair":{"$schema":"https://json-schema.org/draft/2019-09/schema","type":"array","items":[{"type":"string"}]}}}`)
	var schema map[string]any
	if err := json.Unmarshal(NormalizeLegacyTupleItemsForDraft202012(raw), &schema); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := schema["$schema"]; exists {
		t.Fatalf("root gained a $schema it never declared: %v", schema)
	}
	pair := schema["$defs"].(map[string]any)["pair"].(map[string]any)
	if got := pair["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("nested resource $schema = %v, want 2020-12", got)
	}
	if _, ok := pair["prefixItems"].([]any); !ok {
		t.Fatalf("nested prefixItems missing: %v", pair)
	}
}

func TestNormalizeLegacyTupleItemsLeavesUnconvertedDialectAlone(t *testing.T) {
	// No legacy tuple anywhere: the declared old dialect stays, and the exact
	// input bytes come back (no reserialization).
	raw := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"xs":{"type":"array","items":{"type":"string"}}}}`)
	got := NormalizeLegacyTupleItemsForDraft202012(raw)
	if string(got) != string(raw) {
		t.Fatalf("unchanged schema was rewritten:\n in: %s\nout: %s", raw, got)
	}
}

func TestNormalizeLegacyTupleItemsLeavesUnknownDialectAlone(t *testing.T) {
	raw := json.RawMessage(`{"$schema":"https://example.com/custom-dialect","type":"array","items":[{"type":"string"}]}`)
	var schema map[string]any
	if err := json.Unmarshal(NormalizeLegacyTupleItemsForDraft202012(raw), &schema); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := schema["$schema"]; got != "https://example.com/custom-dialect" {
		t.Fatalf("$schema = %v, custom dialects must not be rewritten", got)
	}
	if _, ok := schema["prefixItems"].([]any); !ok {
		t.Fatalf("prefixItems missing: %v", schema)
	}
}

func TestNormalizeLegacyTupleItemsCanonicalByteFastPath(t *testing.T) {
	// No "items" keyword at all: the very same backing bytes come back with no
	// parse — the per-turn cost for the common tool surface must stay zero.
	raw := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	got := NormalizeLegacyTupleItemsForDraft202012(raw)
	if len(got) == 0 || &got[0] != &raw[0] {
		t.Fatal("schema without items must return the input bytes unchanged")
	}

	// Memoized conversion: repeated calls yield identical bytes.
	tuple := json.RawMessage(`{"type":"array","items":[{"type":"string"}]}`)
	first := NormalizeLegacyTupleItemsForDraft202012(tuple)
	second := NormalizeLegacyTupleItemsForDraft202012(tuple)
	if string(first) != string(second) {
		t.Fatalf("memoized conversion diverged:\n1: %s\n2: %s", first, second)
	}
	if string(first) == string(tuple) {
		t.Fatalf("tuple schema was not converted: %s", first)
	}
}
