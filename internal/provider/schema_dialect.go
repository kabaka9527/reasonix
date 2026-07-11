package provider

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
)

// IsMiMoEndpoint reports whether rawURL points at an official Xiaomi MiMo API
// host, including the regional token-plan subdomains. The bare apex is rejected
// because it is not an API endpoint.
func IsMiMoEndpoint(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "xiaomimimo.com" && strings.HasSuffix(host, ".xiaomimimo.com")
}

// normalizedSchemaCache memoizes NormalizeLegacyTupleItemsForDraft202012
// results keyed by the input bytes. Tool schemas come from the registry's
// one-time canonicalization and are byte-stable across turns, while the MiMo
// builders call the normalizer for every tool on every request — without the
// memo each turn re-parses the full tool surface (~hundreds of schemas). The
// cache is bounded by the set of distinct schemas seen and the function is
// pure, so process-wide sharing is safe and deterministic.
var normalizedSchemaCache sync.Map // string -> json.RawMessage

// NormalizeLegacyTupleItemsForDraft202012 rewrites only the pre-2020-12 tuple
// keywords in a JSON Schema. It is intentionally separate from
// CanonicalizeSchema: provider implementations must opt in only after the target
// endpoint's schema dialect is known, so other vendors keep their original tool
// schema bytes and cache prefixes. A schema that needs no rewrite is returned
// with its input bytes unchanged, byte for byte.
func NormalizeLegacyTupleItemsForDraft202012(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	// Legacy tuple syntax cannot exist without an "items" keyword; the common
	// no-op case skips even the parse.
	if !bytes.Contains(raw, []byte(`"items"`)) {
		return raw
	}
	if cached, ok := normalizedSchemaCache.Load(string(raw)); ok {
		return cached.(json.RawMessage)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}
	result := raw
	if normalizeDraft202012Schema(schema) {
		if out, err := json.Marshal(schema); err == nil {
			result = json.RawMessage(out)
		}
	}
	normalizedSchemaCache.Store(string(raw), result)
	return result
}

// normalizeDraft202012Schema rewrites legacy tuple keywords in place and
// reports whether anything changed. When a schema resource — an object
// carrying its own $schema declaration — saw a conversion anywhere in its
// subtree, an old-draft declaration is updated to 2020-12: leaving it would
// make the output self-contradictory (an older dialect declared over
// prefixItems / 2020-12 items), and MCP consumers may then apply the wrong
// tuple semantics.
func normalizeDraft202012Schema(value any) bool {
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false

	for _, keyword := range []string{
		"additionalItems", "additionalProperties", "contains", "contentSchema",
		"else", "if", "items", "not", "propertyNames", "then",
		"unevaluatedItems", "unevaluatedProperties",
	} {
		if normalizeDraft202012Schema(schema[keyword]) {
			changed = true
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if children, ok := schema[keyword].([]any); ok {
			for _, child := range children {
				if normalizeDraft202012Schema(child) {
					changed = true
				}
			}
		}
	}
	for _, keyword := range []string{
		"$defs", "definitions", "dependentSchemas", "patternProperties", "properties",
	} {
		if children, ok := schema[keyword].(map[string]any); ok {
			for _, child := range children {
				if normalizeDraft202012Schema(child) {
					changed = true
				}
			}
		}
	}
	if dependencies, ok := schema["dependencies"].(map[string]any); ok {
		for _, child := range dependencies {
			if normalizeDraft202012Schema(child) {
				changed = true
			}
		}
	}

	if legacyItems, ok := schema["items"].([]any); ok {
		for _, child := range legacyItems {
			normalizeDraft202012Schema(child)
		}
		changed = true
		delete(schema, "items")
		if len(legacyItems) > 0 {
			// Keep an explicit 2020-12 prefix if a malformed mixed-dialect schema
			// contains both forms.
			if _, exists := schema["prefixItems"]; !exists {
				schema["prefixItems"] = legacyItems
			}
		}
		if additional, exists := schema["additionalItems"]; exists {
			delete(schema, "additionalItems")
			if isSchemaObjectOrBool(additional) {
				schema["items"] = additional
			}
		}
	}

	if changed {
		if decl, ok := schema["$schema"].(string); ok && isLegacyJSONSchemaDialect(decl) {
			schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		}
	}
	return changed
}

// isLegacyJSONSchemaDialect reports whether decl names a pre-2020-12 JSON
// Schema dialect. Unknown or custom dialect URIs are left untouched — this
// normalizer only understands the official drafts' tuple semantics.
func isLegacyJSONSchemaDialect(decl string) bool {
	d := strings.TrimSuffix(strings.TrimSpace(decl), "#")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "https://")
	switch d {
	case "json-schema.org/schema",
		"json-schema.org/draft-03/schema",
		"json-schema.org/draft-04/schema",
		"json-schema.org/draft-06/schema",
		"json-schema.org/draft-07/schema",
		"json-schema.org/draft/2019-09/schema":
		return true
	}
	return false
}

func isSchemaObjectOrBool(value any) bool {
	switch value.(type) {
	case map[string]any, bool:
		return true
	default:
		return false
	}
}
