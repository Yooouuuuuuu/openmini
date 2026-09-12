package agyapi

import (
	"reflect"
	"testing"
)

// fixture is the shape of fetchAvailableModels, trimmed to what parseModels
// reads: the sorted groups, the renamed ids and the models map.
var fixture = map[string]any{
	"agentModelSorts": []any{map[string]any{"groups": []any{map[string]any{"modelIds": []any{
		"gemini-3.6-flash-high", "gemini-3.6-flash-low", "gemini-pro-agent", "gemini-3.1-pro-low", "claude-sonnet-4-6",
	}}}}},
	"deprecatedModelIds": map[string]any{"gemini-3.1-pro-high": map[string]any{"newModelId": "gemini-pro-agent"}},
	"models": map[string]any{
		"gemini-3.6-flash-low":    map[string]any{"supportsThinking": true},
		"gemini-3.6-flash-tiered": map[string]any{"supportsThinking": true}, // 3.6 has explicit levels: -low stays as listed
		"gemini-3.7-flash-tiered": map[string]any{"supportsThinking": true},
		"gemini-3.8-flash-tiered": map[string]any{"supportsThinking": true},
		"gemini-2-flash-tiered":   map[string]any{"supportsThinking": false}, // no thinking: no levels to offer
		"gemini-3.1-pro-low":      map[string]any{"supportsThinking": true},
	},
}

func TestParseModels(t *testing.T) {
	ids, deprecated, tiered := parseModels(fixture)
	wantIDs := []string{
		"claude-sonnet-4-6",
		"gemini-3.1-pro-low",
		"gemini-3.6-flash-high", "gemini-3.6-flash-low", "gemini-3.6-flash-medium",
		"gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium",
		"gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-3.8-flash-medium",
		"gemini-pro-agent",
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Errorf("ids = %v\nwant %v", ids, wantIDs)
	}
	if deprecated["gemini-3.1-pro-high"] != "gemini-pro-agent" {
		t.Errorf("deprecated = %v", deprecated)
	}
	if got := tiered["gemini-3.7-flash-low"]; got != (tieredLevel{"gemini-3.7-flash-tiered", "low"}) {
		t.Errorf("tiered 3.7 low = %+v", got)
	}
	if got := tiered["gemini-3.8-flash-high"]; got != (tieredLevel{"gemini-3.8-flash-tiered", "high"}) {
		t.Errorf("tiered 3.8 high = %+v", got)
	}
	// 3.6 Flash: the listed -low and -high stay the service's own ids; only the
	// missing -medium is filled from the tiered model.
	if _, ok := tiered["gemini-3.6-flash-low"]; ok {
		t.Errorf("gemini-3.6-flash-low should stay the explicit id, not a tiered alias")
	}
	if got := tiered["gemini-3.6-flash-medium"]; got != (tieredLevel{"gemini-3.6-flash-tiered", "medium"}) {
		t.Errorf("tiered 3.6 medium = %+v", got)
	}
	if _, ok := tiered["gemini-2-flash-low"]; ok {
		t.Errorf("a model without thinking must not get levels")
	}
}

func TestTranslateAndAccepts(t *testing.T) {
	a := &AgyAPI{}
	a.models, a.deprecated, a.tiered = parseModels(fixture)
	cases := []struct{ in, model, level string }{
		{"gemini-3.7-flash-low", "gemini-3.7-flash-tiered", "low"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-tiered", "high"},
		{"gemini-3.1-pro-high", "gemini-pro-agent", ""},
		{"gemini-3.6-flash-low", "gemini-3.6-flash-low", ""},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", ""},
	}
	for _, c := range cases {
		model, level := a.translate(c.in)
		if model != c.model || level != c.level {
			t.Errorf("translate(%q) = %q, %q; want %q, %q", c.in, model, level, c.model, c.level)
		}
		if !a.Accepts(c.in) {
			t.Errorf("Accepts(%q) = false", c.in)
		}
	}
	if a.Accepts("gemini-9-flash-low") {
		t.Errorf("Accepts(unknown) = true")
	}
}
