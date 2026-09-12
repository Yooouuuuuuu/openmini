package agyapi

import (
	"reflect"
	"strings"
	"testing"

	"openmini/internal/backend"
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

// sse builds the service's event stream from JSON event bodies.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestReadStreamToolCall(t *testing.T) {
	tool := &backend.Transport{Name: "openmini_reply", Param: "content"}
	stream := sse(
		`{"response":{"candidates":[{"content":{"parts":[{"text":"thinking...","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"stray preface"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"openmini_reply","args":{"content":"The whole reply."}}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3,"totalTokenCount":18}}}`,
	)
	var phases []string
	var last string
	st, err := readStream(strings.NewReader(stream), tool, func(p, _ string) { phases = append(phases, p) }, func(s string) { last = s })
	if err != nil {
		t.Fatal(err)
	}
	if !st.viaTool || st.text != "The whole reply." || st.finish != "STOP" || st.stray != len("stray preface") {
		t.Errorf("got %+v", st)
	}
	if last != "The whole reply." {
		t.Errorf("OnText last = %q", last)
	}
	if st.usage["input_tokens"] != 10 || st.usage["thinking_tokens"] != 3 {
		t.Errorf("usage = %v", st.usage)
	}
	if len(phases) != 1 || phases[0] != "generating" {
		t.Errorf("phases = %v", phases)
	}
}

func TestReadStreamPlainText(t *testing.T) {
	stream := sse(
		`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello, "}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"world."}]},"finishReason":"STOP"}]}}`,
	)
	var seen []string
	st, err := readStream(strings.NewReader(stream), nil, nil, func(s string) { seen = append(seen, s) })
	if err != nil {
		t.Fatal(err)
	}
	if st.viaTool || st.text != "Hello, world." || st.finish != "STOP" {
		t.Errorf("got %+v", st)
	}
	if !reflect.DeepEqual(seen, []string{"Hello, ", "Hello, world."}) {
		t.Errorf("OnText saw %q", seen)
	}
	// A call to a function that is not the transport (or with no transport
	// declared) is not a reply.
	stream = sse(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"other","args":{"content":"x"}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)
	st, _ = readStream(strings.NewReader(stream), &backend.Transport{Name: "openmini_reply", Param: "content"}, nil, nil)
	if st.viaTool || st.text != "" || st.finish != "MALFORMED_FUNCTION_CALL" {
		t.Errorf("got %+v", st)
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		text, finish string
		want         bool
	}{
		{"", "MALFORMED_FUNCTION_CALL", true},
		{"", "STOP", true},
		{"", "", true},
		{"partial story", "PROHIBITED_CONTENT", false}, // the filter cut it: retrying only spends quota
		{"", "PROHIBITED_CONTENT", false},
		{"fine", "STOP", false},
		{"", "MAX_TOKENS", false},
	}
	for _, c := range cases {
		if got := retryable(c.text, c.finish); got != c.want {
			t.Errorf("retryable(%q, %q) = %v, want %v", c.text, c.finish, got, c.want)
		}
	}
}
