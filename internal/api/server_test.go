package api

import (
	"encoding/json"
	"errors"
	"testing"

	"openmini/internal/backend"
)

// fake is a backend with a fixed model list; withAccept adds an Accepter.
type fake struct {
	name   string
	models []string
}

func (f fake) Name() string                                  { return f.name }
func (f fake) Ready() error                                  { return nil }
func (f fake) Models() []string                              { return f.models }
func (f fake) StableStream() bool                            { return true }
func (f fake) Complete(backend.Call) (backend.Result, error) { return backend.Result{Text: "ok"}, nil }
func (f fake) Usage() (any, error)                           { return nil, nil }

type withAccept struct {
	fake
	extra map[string]bool
}

func (w withAccept) Accepts(id string) bool { return w.extra[id] }

func TestEquivalent(t *testing.T) {
	api := withAccept{
		fake:  fake{"agyapi", []string{"gemini-3.6-flash-low", "gemini-3.1-pro-low", "claude-sonnet-4-6"}},
		extra: map[string]bool{"gemini-3.1-pro-high": true, "gemini-3.7-flash-low": true},
	}
	web := fake{"web", []string{"3.1 Pro", "3.8 Flash", "3.5 Flash-Lite", "延伸思考"}}
	cases := []struct {
		b    backend.Backend
		id   string
		want string
		ok   bool
	}{
		{api, "gemini-3.6-flash-low", "gemini-3.6-flash-low", true}, // listed
		{api, "Claude-Sonnet-4-6", "claude-sonnet-4-6", true},       // listed, case-insensitively
		{api, "gemini-3.1-pro-high", "gemini-3.1-pro-high", true},   // accepted though unlisted (renamed by the service)
		{api, "gemini-3.7-flash-low", "gemini-3.7-flash-low", true}, // accepted though unlisted (tiered)
		{api, "gemini-3.9-flash-low", "", false},                    // unknown: no reroute
		{web, "gemini-3.8-flash-low", "3.8 Flash", true},            // label by family, level dropped
		{web, "gemini-3.1-pro-high", "3.1 Pro", true},
		{web, "gemini-3.5-flash-lite", "3.5 Flash-Lite", true},
		{web, "claude-sonnet-4-6", "", false}, // the web picker has no Claude
	}
	for _, c := range cases {
		got, ok := equivalent(c.b, c.id)
		if got != c.want || ok != c.ok {
			t.Errorf("equivalent(%s, %q) = %q, %v; want %q, %v", c.b.Name(), c.id, got, ok, c.want, c.ok)
		}
	}
}

func TestRefusingKeepsNameFailsComplete(t *testing.T) {
	want := errors.New("no")
	r := refusing{fake{"agy", nil}, want}
	if r.Name() != "agy" {
		t.Fatalf("name %q", r.Name())
	}
	if _, err := r.Complete(backend.Call{}); err != want {
		t.Fatalf("Complete err = %v", err)
	}
}

func TestTransportTool(t *testing.T) {
	parse := func(body string) []chatTool {
		var r chatRequest
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		return r.Tools
	}
	// The shape an anti-truncation preset sends.
	emit := `{"tools":[{"type":"function","function":{"name":"emit_complete_response_dde83b1754d651a0126a96c3","description":"Emit the complete final reply.","parameters":{"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}}}],"tool_choice":"auto"}`
	got := transportTool(parse(emit))
	want := &backend.Transport{Name: "emit_complete_response_dde83b1754d651a0126a96c3", Description: "Emit the complete final reply.", Param: "content"}
	if got == nil || *got != *want {
		t.Errorf("emit tool: got %+v, want %+v", got, want)
	}
	for name, body := range map[string]string{
		"no tools":      `{"tools":[]}`,
		"absent":        `{"model":"x"}`,
		"two tools":     `{"tools":[{"type":"function","function":{"name":"a","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}},{"type":"function","function":{"name":"b","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]}`,
		"two params":    `{"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"q":{"type":"string"},"n":{"type":"integer"}}}}}]}`,
		"non-string":    `{"tools":[{"type":"function","function":{"name":"count","parameters":{"type":"object","properties":{"n":{"type":"integer"}}}}}]}`,
		"no parameters": `{"tools":[{"type":"function","function":{"name":"ping"}}]}`,
	} {
		if got := transportTool(parse(body)); got != nil {
			t.Errorf("%s: got %+v, want nil", name, got)
		}
	}
}
