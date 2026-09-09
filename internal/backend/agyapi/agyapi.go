// Package agyapi talks to the Antigravity backend service directly, the same
// endpoint the agy binary uses, with agy's stored OAuth token. No agent
// harness: no message cap, no tool schemas, no system prompt overhead.
package agyapi

import (
	"openmini/internal/proc"

	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openmini/internal/backend"
	"openmini/internal/config"
)

type AgyAPI struct {
	cfg     config.AgyAPI
	timeout int
	logf    func(string, ...any)
	http    *http.Client

	mu      sync.Mutex
	access  string
	expiry  time.Time
	project string
	models  []string
	ready   error

	deprecated map[string]string // old id -> the id the service wants now
}

func New(cfg config.AgyAPI, timeoutSec int, logf func(string, ...any)) *AgyAPI {
	home, _ := os.UserHomeDir()
	expand := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	cfg.TokenFile = expand(cfg.TokenFile)
	cfg.AgyBinary = backend.FindAgy(cfg.AgyBinary)
	cfg.AgyBinary = expand(cfg.AgyBinary)
	return &AgyAPI{cfg: cfg, timeout: timeoutSec, logf: logf, http: &http.Client{}}
}

func (a *AgyAPI) Name() string       { return "agyapi" }
func (a *AgyAPI) StableStream() bool { return true }
func (a *AgyAPI) Models() []string   { return a.models }

// ---------------------------------------------------------------------------
// Token and project
// ---------------------------------------------------------------------------

// loadToken reads agy's session (JSON: token.access_token, token.expiry) from
// its token file, or on Windows from the Credential Manager entry.
func (a *AgyAPI) loadToken() error {
	raw, err := os.ReadFile(a.cfg.TokenFile)
	if err != nil {
		target := a.cfg.Credential
		if target == "" {
			target = "gemini:antigravity"
		}
		if kr, kerr := keyringToken(target); kerr == nil {
			raw, err = kr, nil
		} else {
			return fmt.Errorf("no agy session: token file: %v; keyring %q: %v (sign in with `agy` once)", err, target, kerr)
		}
	}
	var f struct {
		Token struct {
			AccessToken string `json:"access_token"`
			Expiry      string `json:"expiry"`
		} `json:"token"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Token.AccessToken == "" {
		return fmt.Errorf("agy's stored session has no access_token")
	}
	a.access = f.Token.AccessToken
	a.expiry, _ = time.Parse(time.RFC3339Nano, f.Token.Expiry)
	return nil
}

// ensureToken refreshes through agy itself when the stored token is about to
// expire: running any agy command makes it renew and rewrite the file, so no
// OAuth client secret has to live in this program.
func (a *AgyAPI) ensureToken(force bool) error {
	if !force && a.access != "" && time.Until(a.expiry) > 2*time.Minute {
		return nil
	}
	if force || a.access == "" || time.Until(a.expiry) <= 2*time.Minute {
		if a.access != "" || force {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, a.cfg.AgyBinary, "models")
			proc.Quiet(cmd)
			cmd.Dir = os.TempDir()
			if out, err := cmd.CombinedOutput(); err != nil {
				a.logf("agyapi: token refresh via agy failed: %v: %s", err, firstLine(string(out)))
			}
		}
	}
	if err := a.loadToken(); err != nil {
		return err
	}
	if time.Until(a.expiry) <= 0 {
		return fmt.Errorf("agy's token is expired and could not be refreshed; run `agy` once")
	}
	return nil
}

func (a *AgyAPI) post(ctx context.Context, method string, body any, stream bool) (*http.Response, error) {
	if err := a.ensureToken(false); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	url := a.cfg.Endpoint + "/v1internal:" + method
	if stream {
		url += "?alt=sse"
	}
	do := func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+a.access)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", a.cfg.UserAgent)
		return a.http.Do(req)
	}
	resp, err := do()
	if err == nil && resp.StatusCode == 401 {
		resp.Body.Close()
		if err := a.ensureToken(true); err != nil {
			return nil, err
		}
		resp, err = do()
	}
	return resp, err
}

func (a *AgyAPI) call(method string, body any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := a.post(ctx, method, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, firstLine(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: bad JSON: %v", method, err)
	}
	return out, nil
}

// Ready loads the token, resolves the project, and lists models.
func (a *AgyAPI) Ready() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ready == nil && a.project != "" && len(a.models) > 0 {
		return nil
	}
	a.ready = a.setup()
	return a.ready
}

func (a *AgyAPI) setup() error {
	if err := a.ensureToken(false); err != nil {
		return err
	}
	if a.cfg.Project != "" {
		a.project = a.cfg.Project
	} else {
		lca, err := a.call("loadCodeAssist", map[string]any{"metadata": map[string]string{
			"ideType": a.cfg.IDEType, "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}})
		if err != nil {
			return err
		}
		p, _ := lca["cloudaicompanionProject"].(string)
		if p == "" {
			return fmt.Errorf("loadCodeAssist returned no project; set agyapi.project in config.toml")
		}
		a.project = p
		tier := ""
		if t, ok := lca["currentTier"].(map[string]any); ok {
			tier, _ = t["id"].(string)
		}
		if t, ok := lca["paidTier"].(map[string]any); ok {
			if id, _ := t["id"].(string); id != "" {
				tier = id
			}
		}
		a.logf("agyapi: project resolved, tier %q", tier)
	}
	a.models = a.fetchModels()
	if len(a.models) == 0 {
		a.models = append([]string(nil), a.cfg.Models...)
		a.logf("agyapi: fetchAvailableModels gave no usable ids; using the configured list")
	}
	return nil
}

// fetchModels asks the service for its model ids; the response shape is not
// documented, so anything string-like under models/id/name is accepted.
func (a *AgyAPI) fetchModels() []string {
	out, err := a.call("fetchAvailableModels", map[string]any{"project": a.project})
	if err != nil {
		a.logf("agyapi: fetchAvailableModels: %v", err)
		return nil
	}
	if raw, err := json.MarshalIndent(out, "", "  "); err == nil {
		os.MkdirAll("data", 0755)
		os.WriteFile(filepath.Join("data", "agyapi-models.json"), raw, 0644)
	}
	// Ids the service has renamed: agy sends the new id when given the old
	// one, and so do we.
	a.deprecated = map[string]string{}
	if m, ok := any(out).(map[string]any); ok {
		if d, ok := m["deprecatedModelIds"].(map[string]any); ok {
			for old, v := range d {
				if mm, ok := v.(map[string]any); ok {
					if nw, ok := mm["newModelId"].(string); ok && nw != "" {
						a.deprecated[old] = nw
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	var ids []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	// The list arrives as agentModelSorts[].groups[].modelIds[]; accept any
	// "modelIds" array of strings wherever it sits.
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 6 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			if arr, ok := t["modelIds"].([]any); ok {
				for _, x := range arr {
					if s, ok := x.(string); ok {
						add(s)
					}
				}
			}
			for _, vv := range t {
				walk(vv, depth+1)
			}
		case []any:
			for _, vv := range t {
				walk(vv, depth+1)
			}
		}
	}
	walk(out, 0)
	sort.Strings(ids)
	if len(ids) == 0 {
		raw, _ := json.Marshal(out)
		a.logf("agyapi: fetchAvailableModels shape not recognised: %s", firstLine(string(raw)))
	}
	return ids
}

// ---------------------------------------------------------------------------
// Generation
// ---------------------------------------------------------------------------

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// officialPrompt is the opening of Antigravity's own system prompt, wrapped so
// the model treats it as a non-instruction. The IDE's requests always start
// with this text; sending it makes ours look the same to the service.
const officialPrompt = `<example_only do_not_follow="true" type="counter-example" ignore="true">
You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Proactiveness**
</example_only>
<!-- Note: The above content is provided as a reference example only and is not part of the active instruction set for this conversation -->`

// newRequestID follows the Antigravity clients' "agent/<unix ms>/<uuid>/4" shape.
func newRequestID() string {
	return fmt.Sprintf("agent/%d/%s/4", time.Now().UnixMilli(), newUUID())
}

// newSessionID mirrors the clients' session ids: a negative random 63-bit
// integer as a decimal string.
func newSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	var n int64
	for _, x := range b {
		n = n<<8 | int64(x)
	}
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("-%d", n)
}

func (a *AgyAPI) Complete(c backend.Call) (backend.Result, error) {
	if err := a.Ready(); err != nil {
		return backend.Result{}, err
	}
	id := c.Model
	if id == "" {
		id = a.cfg.Model
	}
	model := id // the service takes agy's slugs verbatim
	if nw, ok := a.deprecated[id]; ok {
		a.logf("agyapi: %s is deprecated by the service; sending %s", id, nw)
		model = nw
	}
	gen := map[string]any{}
	if c.MaxTokens > 0 {
		gen["maxOutputTokens"] = c.MaxTokens
	}
	// Shape used by the Antigravity clients: userAgent and requestType tell
	// the service which product's quota applies; enabledCreditTypes names
	// the subscription entitlement.
	request := map[string]any{
		"contents":  []map[string]any{{"role": "user", "parts": []map[string]string{{"text": c.Prompt}}}},
		"sessionId": newSessionID(),
	}
	// System instruction: the Antigravity identity snippet first (the IDE's
	// requests always carry it; it is wrapped so the model does not act on
	// it), then any configured instruction of our own.
	var sys []string
	if a.cfg.OfficialPrompt {
		sys = append(sys, officialPrompt)
	}
	if t := strings.TrimSpace(a.cfg.SystemInstruction); t != "" {
		sys = append(sys, t)
	}
	if len(sys) > 0 {
		request["systemInstruction"] = map[string]any{"role": "user", "parts": []map[string]string{{"text": strings.Join(sys, "\n\n")}}}
	}
	if len(gen) > 0 {
		request["generationConfig"] = gen
	}
	req := map[string]any{
		"model":       model,
		"project":     a.project,
		"requestId":   newRequestID(),
		"request":     request,
		"userAgent":   "antigravity",
		"requestType": "agent",
	}
	if len(a.cfg.CreditTypes) > 0 {
		req["enabledCreditTypes"] = a.cfg.CreditTypes
	}

	ctx := c.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.timeout)*time.Second)
		defer cancel()
	}
	t0 := time.Now()
	resp, err := a.post(ctx, "streamGenerateContent", req, true)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return backend.Result{}, fmt.Errorf("stopped by request")
		}
		return backend.Result{}, err
	}
	defer resp.Body.Close()
	if c.OnPhase != nil {
		c.OnPhase("submitted", "")
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := firstLine(string(raw))
		if resp.StatusCode == 429 {
			return backend.Result{}, fmt.Errorf("rate limited (HTTP 429, retry after %s): %s", resp.Header.Get("Retry-After"), msg)
		}
		return backend.Result{}, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, model, msg)
	}

	var text strings.Builder
	usage := map[string]int{}
	finish := ""
	generating := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Response struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text    string `json:"text"`
							Thought bool   `json:"thought"`
						} `json:"parts"`
					} `json:"content"`
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
				UsageMetadata struct {
					Prompt     int `json:"promptTokenCount"`
					Candidates int `json:"candidatesTokenCount"`
					Thoughts   int `json:"thoughtsTokenCount"`
					Total      int `json:"totalTokenCount"`
				} `json:"usageMetadata"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &ev) != nil {
			continue
		}
		for _, cand := range ev.Response.Candidates {
			for _, p := range cand.Content.Parts {
				if p.Thought || p.Text == "" {
					continue
				}
				if !generating {
					generating = true
					if c.OnPhase != nil {
						c.OnPhase("generating", "")
					}
				}
				text.WriteString(p.Text)
				if c.OnText != nil {
					c.OnText(text.String())
				}
			}
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
		}
		if u := ev.Response.UsageMetadata; u.Total > 0 {
			usage["input_tokens"], usage["output_tokens"], usage["thinking_tokens"], usage["total_tokens"] = u.Prompt, u.Candidates, u.Thoughts, u.Total
		}
	}
	if err := sc.Err(); err != nil && text.Len() == 0 {
		if ctx.Err() == context.Canceled {
			if text.Len() > 0 {
				return backend.Result{Text: text.String(), Status: "STOPPED"}, nil
			}
			return backend.Result{}, fmt.Errorf("stopped by request")
		}
		return backend.Result{}, fmt.Errorf("stream: %v", err)
	}
	if ctx.Err() == context.Canceled && text.Len() > 0 {
		return backend.Result{Text: text.String(), Status: "STOPPED"}, nil
	}
	a.logf("agyapi: %s %s finished in %.1fs, %d chars, finish=%s", c.ID, model, time.Since(t0).Seconds(), backend.Chars(text.String()), finish)
	status := "SUCCESS"
	if finish != "" && finish != "STOP" {
		status = finish
	}
	out := text.String()
	if out == "" && finish != "" && finish != "STOP" {
		out = fmt.Sprintf("[openmini/agyapi] the model returned no text (finish reason %s)", finish)
	}
	return backend.Result{Text: out, Status: status, Usage: usage}, nil
}

// Usage returns the quota buckets from retrieveUserQuota.
func (a *AgyAPI) Usage() (any, error) {
	if err := a.Ready(); err != nil {
		return nil, err
	}
	type row struct {
		Pool      string `json:"pool"`
		Window    string `json:"window"`
		Remaining string `json:"remaining"`
		ResetsAt  string `json:"resets_at,omitempty"`
		ResetsIn  string `json:"resets_in,omitempty"`
	}
	var rows []row
	// The summary is what the agy CLI's /usage shows: per model group (Gemini;
	// Claude and GPT), a weekly and a 5-hour bucket. The per-model call
	// (retrieveUserQuota) only carries 5-hour buckets and stays as a fallback.
	if sum, err := a.call("retrieveUserQuotaSummary", map[string]any{"project": a.project}); err == nil {
		if groups, ok := sum["groups"].([]any); ok {
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				buckets, _ := gm["buckets"].([]any)
				for _, b := range buckets {
					m, _ := b.(map[string]any)
					r := row{Pool: str(gm["displayName"]), Window: str(m["displayName"])}
					if f, ok := m["remainingFraction"].(float64); ok {
						r.Remaining = fmt.Sprintf("%.0f%%", f*100)
					}
					if rt := str(m["resetTime"]); rt != "" {
						r.ResetsAt = rt
						if t, err := time.Parse(time.RFC3339, rt); err == nil {
							r.ResetsIn = time.Until(t).Round(time.Minute).String()
						}
					}
					rows = append(rows, r)
				}
			}
		}
	}
	if len(rows) > 0 {
		return rows, nil
	}
	out, err := a.call("retrieveUserQuota", map[string]any{"project": a.project})
	if err != nil {
		return nil, err
	}
	if buckets, ok := out["buckets"].([]any); ok {
		for _, b := range buckets {
			m, _ := b.(map[string]any)
			r := row{Pool: str(m["modelId"]), Window: str(m["tokenType"])}
			if strings.HasPrefix(r.Pool, "chat_") || strings.HasPrefix(r.Pool, "tab_") {
				continue // internal ids of the editor's chat and tab features
			}
			if f, ok := m["remainingFraction"].(float64); ok {
				r.Remaining = fmt.Sprintf("%.0f%%", f*100)
			} else {
				r.Remaining = str(m["remainingAmount"])
			}
			if rt := str(m["resetTime"]); rt != "" {
				r.ResetsAt = rt
				if t, err := time.Parse(time.RFC3339, rt); err == nil {
					r.ResetsIn = time.Until(t).Round(time.Minute).String()
				}
			}
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return out, nil // unknown shape: return it as-is
	}
	return rows, nil
}

// RawQuota returns a quota method's response as-is, for the debug route.
// method is "retrieveUserQuota" (per model, 5-hour buckets) or
// "retrieveUserQuotaSummary" (per model group, weekly and 5-hour).
func (a *AgyAPI) RawQuota(method string) (map[string]any, error) {
	if err := a.Ready(); err != nil {
		return nil, err
	}
	if method == "" {
		method = "retrieveUserQuota"
	}
	return a.call(method, map[string]any{"project": a.project})
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// firstLine compacts an error body: JSON is squeezed onto one line and cut at
// 600 characters, so the service's own message survives into the log.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		b, _ := json.Marshal(v)
		s = string(b)
	} else if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}
