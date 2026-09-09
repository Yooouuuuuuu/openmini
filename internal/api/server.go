// Package api exposes the backends as an OpenAI-compatible HTTP server and
// tracks the state of every request.
package api

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"

	"openmini/internal/backend"
	"openmini/internal/backend/agy"
	"openmini/internal/backend/web"
	"openmini/internal/config"
	"openmini/internal/history"
	"openmini/internal/logging"
	"openmini/internal/state"
)

//go:embed ui/index.html
var uiHTML string

// usageEntry is the last known usage of one backend; refreshed only on request.
type usageEntry struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	At    string `json:"at"`
}

type Server struct {
	usageMu    sync.Mutex
	usageCache map[string]usageEntry
	recentMu   sync.Mutex
	recent     []chatRequest // last few request bodies, memory only, for /tools/policy-bisect?last=1
	cfg        *config.Config
	log        *logging.Logger
	st         *state.Registry
	backends   map[string]backend.Backend
	webDebug   *web.Web
	started    time.Time
	OnStop     func()         // asked to shut down by POST /shutdown from this machine
	hist       *history.Store // one file per request, when [history] is on
}

func New(cfg *config.Config, log *logging.Logger, backends map[string]backend.Backend, webDebug *web.Web) *Server {
	var hist *history.Store
	if cfg.History.Enabled {
		h, err := history.New(cfg.History.Directory)
		if err != nil {
			log.Printf("history: %v (not recording)", err)
		} else {
			hist = h
			log.Printf("history: writing one file per request to %s", cfg.History.Directory)
		}
	}
	return &Server{hist: hist, cfg: cfg, log: log, st: state.New(), backends: backends, webDebug: webDebug, started: time.Now(), usageCache: map[string]usageEntry{}}
}

func (s *Server) Listen() error {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, BodyLimit: 1024 * 1024 * 1024})
	// log every request that does not succeed: wrong paths and bad keys are
	// otherwise invisible, and clients report them as "refused"
	app.Use(func(c *fiber.Ctx) error {
		err := c.Next()
		st := c.Response().StatusCode()
		if e, ok := err.(*fiber.Error); ok {
			st = e.Code // unmatched routes report their status through the error
		}
		if st >= 400 || err != nil {
			s.log.Printf("%s %s -> %d from %s", c.Method(), c.OriginalURL(), st, c.IP())
		}
		return err
	})
	app.Use(s.auth)
	app.Get("/", func(c *fiber.Ctx) error { return c.Redirect("/usage", 302) })
	app.Get("/usage", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(uiHTML)
	})
	app.Get("/usage/cached", s.handleUsageCached)
	app.Post("/usage/refresh", s.handleUsageRefresh)
	app.Get("/usage/refresh", s.handleUsageRefresh)
	app.Get("/health", s.handleHealth)
	app.Get("/status", s.handleStatus)
	app.Get("/status/stream", s.handleStatusStream)
	app.Post("/requests/stop", s.handleStop)
	app.Post("/shutdown", s.handleShutdown)
	app.Get("/v1/models", s.handleModels)
	app.Post("/v1/chat/completions", s.handleChat)
	// the same endpoints with every model a backend has, not just the curated set
	app.Get("/all/v1/models", s.all(s.handleModels))
	app.Post("/all/v1/chat/completions", s.all(s.handleChat))
	app.Post("/tools/policy-bisect", s.handlePolicyBisect)
	app.Get("/debug/agyapi/quota", func(c *fiber.Ctx) error {
		b, ok := s.backends["agyapi"]
		if !ok {
			return c.Status(404).JSON(fiber.Map{"error": "agyapi not enabled"})
		}
		q, ok := b.(interface {
			RawQuota(string) (map[string]any, error)
		})
		if !ok {
			return c.Status(500).JSON(fiber.Map{"error": "no raw quota"})
		}
		out, err := q.RawQuota(c.Query("method"))
		if err != nil {
			return c.Status(502).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(out)
	})
	if s.webDebug != nil {
		app.Get("/debug/web/html", func(c *fiber.Ctx) error {
			h, err := s.webDebug.HTML()
			if err != nil {
				return c.Status(500).SendString(err.Error())
			}
			c.Set("Content-Type", "text/plain; charset=utf-8")
			return c.SendString(h)
		})
		app.Post("/web/reload", func(c *fiber.Ctx) error {
			if err := s.webDebug.Reload(); err != nil {
				return c.Status(500).JSON(fiber.Map{"ok": false, "error": err.Error()})
			}
			return c.JSON(fiber.Map{"ok": true})
		})
		app.Get("/debug/web/settings", func(c *fiber.Ctx) error {
			h, err := s.webDebug.SettingsMenuHTML(c.Query("item"))
			if err != nil {
				return c.Status(500).SendString(err.Error())
			}
			c.Set("Content-Type", "text/plain; charset=utf-8")
			return c.SendString(h)
		})
		app.Get("/debug/web/laststream", func(c *fiber.Ctx) error {
			b, u := s.webDebug.LastStream()
			c.Set("Content-Type", "text/plain; charset=utf-8")
			c.Set("X-Stream-Url", u)
			return c.Send(b)
		})
		app.Get("/debug/web/copy", func(c *fiber.Ctx) error {
			t, err := s.webDebug.CopyLastReply()
			if err != nil {
				return c.Status(500).SendString(err.Error())
			}
			c.Set("Content-Type", "text/plain; charset=utf-8")
			return c.SendString(t)
		})
		app.Get("/debug/web/screenshot", func(c *fiber.Ctx) error {
			img, err := s.webDebug.Screenshot()
			if err != nil {
				return c.Status(500).SendString(err.Error())
			}
			c.Set("Content-Type", "image/png")
			return c.Send(img)
		})
	}
	return app.Listen(fmt.Sprintf(":%d", s.cfg.Server.Port))
}

// auth enforces api_keys when configured; /health stays open.
func (s *Server) auth(c *fiber.Ctx) error {
	if len(s.cfg.Server.APIKeys) == 0 || c.Path() == "/health" {
		return c.Next()
	}
	key := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
	for _, k := range s.cfg.Server.APIKeys {
		if k != "" && key == k {
			return c.Next()
		}
	}
	return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "invalid API key", "type": "invalid_request_error"}})
}

func (s *Server) handleHealth(c *fiber.Ctx) error {
	ready := fiber.Map{}
	for name, b := range s.backends {
		if err := b.Ready(); err != nil {
			ready[name] = err.Error()
		} else {
			ready[name] = "ok"
		}
	}
	return c.JSON(fiber.Map{"ok": true, "uptime_s": int(time.Since(s.started).Seconds()), "backends": ready})
}

// handleStatus reports what every request is doing right now.
// statusPayload is what /status and /status/stream send.
func (s *Server) statusPayload() fiber.Map {
	active, recent := s.st.Snapshot()
	if active == nil {
		active = []state.Request{}
	}
	if recent == nil {
		recent = []state.Request{}
	}
	return fiber.Map{"active": active, "recent": recent}
}

// handleStatusStream pushes the status to an open dashboard as server-sent
// events: once on connect, then whenever a request changes (bursts coalesced
// to a few per second), with a comment every 20s so idle proxies keep the
// connection. Nothing is polled.
func (s *Server) handleStatusStream(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	ch, stop := s.st.Subscribe()
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer stop()
		send := func() bool {
			b, err := json.Marshal(s.statusPayload())
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			return w.Flush() == nil
		}
		if !send() {
			return
		}
		keep := time.NewTicker(20 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-ch:
				if !send() {
					return
				}
				time.Sleep(250 * time.Millisecond) // coalesce bursts of updates
			case <-keep.C:
				if _, err := fmt.Fprint(w, ": keep\n\n"); err != nil || w.Flush() != nil {
					return
				}
			}
		}
	})
	return nil
}

func (s *Server) handleStatus(c *fiber.Ctx) error {
	active, recent := s.st.Snapshot()
	if active == nil {
		active = []state.Request{}
	}
	if recent == nil {
		recent = []state.Request{}
	}
	if c.Query("format") == "text" {
		var b strings.Builder
		fmt.Fprintf(&b, "active: %d\n", len(active))
		for _, r := range active {
			fmt.Fprintf(&b, "  %s  %-4s %-22s %-11s %6.0fs  prompt %d  reply %d  %s\n", r.ID, r.Backend, r.Model, r.Phase, r.ElapsedSec, r.PromptChars, r.ReplyChars, r.Note)
		}
		fmt.Fprintf(&b, "recent: %d\n", len(recent))
		for i, r := range recent {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "  %s  %-4s %-22s %-11s %6.0fs  prompt %d  reply %d  %s\n", r.ID, r.Backend, r.Model, r.Phase, r.ElapsedSec, r.PromptChars, r.ReplyChars, r.Note)
		}
		c.Set("Content-Type", "text/plain; charset=utf-8")
		return c.SendString(b.String())
	}
	return c.JSON(fiber.Map{"active": active, "recent": recent, "checked_at": time.Now().Format(time.RFC3339)})
}

// resolve maps a request model name to a backend and its model id.
// "web/3.1 Pro" and "agy/gemini-3.1-pro-low" are explicit; a bare name that
// matches exactly one backend's list routes there; anything else goes to the
// default backend as its default model.
func (s *Server) resolve(name string, all bool) (backend.Backend, string, error) {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '/'); i > 0 {
		if b, ok := s.backends[name[:i]]; ok {
			m := strings.TrimSpace(name[i+1:])
			if m == "default" || m == "" {
				return b, "", nil
			}
			id, err := s.lookup(b, m, all)
			return b, id, err
		}
		return nil, "", fmt.Errorf("unknown backend prefix %q", name[:i])
	}
	var match backend.Backend
	var matchID string
	for _, b := range s.backends {
		_, real := s.offered(b, all)
		if id, ok := real[strings.ToLower(name)]; ok {
			if match != nil && match != b {
				match = nil
				break
			}
			match, matchID = b, id
		}
	}
	if match != nil {
		return match, matchID, nil
	}
	b, ok := s.backends[s.cfg.Server.DefaultBackend]
	if !ok {
		return nil, "", fmt.Errorf("default backend %q is not enabled", s.cfg.Server.DefaultBackend)
	}
	return b, "", nil
}

// all marks a request as made on /all/v1, where nothing is hidden.
func (s *Server) all(h fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals("all", true)
		return h(c)
	}
}

// family strips a trailing thinking level, so gemini-3.6-flash-high and
// gemini-3.6-flash-low are one model with two levels.
func family(id string) (base, level string) {
	low := strings.ToLower(id)
	for _, l := range []string{"-low", "-medium", "-high", "-thinking"} {
		if strings.HasSuffix(low, l) {
			return id[:len(id)-len(l)], l[1:]
		}
	}
	return id, ""
}

// curated is what /v1 offers out of a backend's list: nothing on the hide
// list, and one entry per model, preferring the configured level, then the
// cheaper ones, then whatever the model comes in.
func (s *Server) curated(ids []string) []string {
	hidden := func(id string) bool {
		for _, p := range s.cfg.Models.Hide {
			if strings.EqualFold(p, id) {
				return true
			}
			if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(id)); ok {
				return true
			}
		}
		return false
	}
	rank := func(level string) int {
		order := []string{s.cfg.Models.Level, "low", "medium", "high", "", "thinking"}
		for i, l := range order {
			if l == level {
				return i
			}
		}
		return len(order)
	}
	best := map[string]string{} // family -> chosen id
	var order []string
	for _, id := range ids {
		if hidden(id) {
			continue
		}
		base, level := family(id)
		cur, seen := best[base]
		if !seen {
			best[base] = id
			order = append(order, base)
			continue
		}
		if _, curLevel := family(cur); rank(level) < rank(curLevel) {
			best[base] = id
		}
	}
	out := make([]string, 0, len(order))
	for _, base := range order {
		out = append(out, best[base])
	}
	return out
}

// offered is what a base URL lists for a backend and what each name stands
// for. On /all/v1 that is every id as the backend names it. On /v1 it is the
// curated set under family names (gemini-3.8-flash for gemini-3.8-flash-low,
// claude-opus-4-6 for the -thinking variant); the full curated id is
// accepted there too.
func (s *Server) offered(b backend.Backend, all bool) (names []string, real map[string]string) {
	real = map[string]string{}
	if all {
		for _, id := range b.Models() {
			names = append(names, id)
			real[strings.ToLower(id)] = id
		}
		return
	}
	for _, id := range s.curated(b.Models()) {
		short, _ := family(id)
		names = append(names, short)
		real[strings.ToLower(short)] = id
		real[strings.ToLower(id)] = id
	}
	return
}

// lookup maps a requested name to a backend's real id on the given base
// URL. Unknown names on /all/v1 pass through for the backend to judge; on
// /v1 they are refused with a pointer to /all/v1.
func (s *Server) lookup(b backend.Backend, name string, all bool) (string, error) {
	_, real := s.offered(b, all)
	if id, ok := real[strings.ToLower(name)]; ok {
		return id, nil
	}
	known := false
	for _, m := range b.Models() {
		if strings.EqualFold(m, name) {
			known = true
		}
	}
	if all || !known {
		return name, nil
	}
	return "", fmt.Errorf("model %q is not offered on /v1; the same request works on /all/v1", b.Name()+"/"+name)
}

// probeModel is the cheap model the policy-bisect tool probes with: the
// configured one, else the lowest Flash the backend lists.
func (s *Server) probeModel(b backend.Backend) string {
	if p := s.cfg.AgyAPI.ProbeModel; p != "" {
		return p
	}
	var flash string
	for _, m := range b.Models() {
		l := strings.ToLower(m)
		if !strings.Contains(l, "flash") {
			continue
		}
		if strings.HasSuffix(l, "-low") {
			return m
		}
		if flash == "" {
			flash = m
		}
	}
	return flash
}

// completion is the OpenAI chat completion object (and its streaming chunk)
// with the fields in the order clients and readers expect.
type completion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices any    `json:"choices"` // []choice for a completion, []chunkChoice for a stream chunk
	Usage   any    `json:"usage,omitempty"`
}

type choice struct {
	Index        int      `json:"index"`
	Message      *message `json:"message"`
	FinishReason any      `json:"finish_reason"`
}

// chunkChoice always carries a delta object, empty on the final chunk, as
// clients expect.
type chunkChoice struct {
	Index        int       `json:"index"`
	Delta        fiber.Map `json:"delta"`
	FinishReason any       `json:"finish_reason"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Server) handleModels(c *fiber.Ctx) error {
	var data []fiber.Map
	names := make([]string, 0, len(s.backends))
	for n := range s.backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ids, _ := s.offered(s.backends[n], c.Locals("all") == true)
		for _, m := range ids {
			data = append(data, fiber.Map{"id": n + "/" + m, "object": "model", "created": s.started.Unix(), "owned_by": "openmini"})
		}
	}
	if data == nil {
		data = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"object": "list", "data": data})
}

// Shutdown cancels every active request so the backends stop working on
// them (agy processes are killed, Gemini's stop button is pressed) and waits
// briefly for them to wind down.
func (s *Server) Shutdown() {
	n := s.st.CancelAll()
	if n == 0 {
		return
	}
	s.log.Printf("stopping %d active request(s)", n)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if a, _ := s.st.Snapshot(); len(a) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// handleShutdown stops the server, but only when asked from this machine
// (openmini stop, or the console), never from the network.
func (s *Server) handleShutdown(c *fiber.Ctx) error {
	ip := c.IP()
	if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
		return c.Status(403).JSON(fiber.Map{"error": "shutdown is only accepted from this machine"})
	}
	if s.OnStop == nil {
		return c.Status(500).JSON(fiber.Map{"error": "no stop handler"})
	}
	s.log.Printf("shutdown requested locally")
	go func() {
		time.Sleep(300 * time.Millisecond) // let the reply go out
		s.OnStop()
	}()
	return c.JSON(fiber.Map{"ok": true, "message": "stopping"})
}

// handleStop cancels an active request by id (?id= or JSON {"id":...}).
func (s *Server) handleStop(c *fiber.Ctx) error {
	id := c.Query("id")
	if id == "" {
		var b struct {
			ID string `json:"id"`
		}
		c.BodyParser(&b)
		id = b.ID
	}
	if id == "" {
		return c.Status(400).JSON(fiber.Map{"error": "id required"})
	}
	if s.st.Cancel(id) {
		s.log.Printf("%s stop requested", id)
		return c.JSON(fiber.Map{"ok": true, "id": id})
	}
	return c.Status(404).JSON(fiber.Map{"ok": false, "error": "no active request with that id"})
}

// handleUsageCached returns the last known usage per backend without
// touching any backend.
func (s *Server) handleUsageCached(c *fiber.Ctx) error {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	out := fiber.Map{}
	for k, v := range s.usageCache {
		out[k] = v
	}
	return c.JSON(out)
}

// handleUsageRefresh fetches usage for one backend (?backend=web|agy|agyapi)
// or all of them, stores it, and returns the refreshed entries.
func (s *Server) handleUsageRefresh(c *fiber.Ctx) error {
	want := c.Query("backend")
	out := fiber.Map{}
	for name, b := range s.backends {
		if want != "" && want != name {
			continue
		}
		e := usageEntry{At: time.Now().Format(time.RFC3339)}
		if u, err := b.Usage(); err != nil {
			e.Error = err.Error()
		} else {
			e.Data = u
		}
		s.usageMu.Lock()
		s.usageCache[name] = e
		s.usageMu.Unlock()
		out[name] = e
	}
	if want != "" && out[want] == nil {
		return c.Status(404).JSON(fiber.Map{"error": "unknown or disabled backend " + want})
	}
	return c.JSON(out)
}

// ---------------------------------------------------------------------------
// Chat completions
// ---------------------------------------------------------------------------

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// buildPrompt turns the messages into the full prompt, the context without the
// final user message, and that final user message.
func (s *Server) buildPrompt(msgs []chatMessage) (full, context, lastUser string, err error) {
	if len(msgs) == 0 {
		return "", "", "", fmt.Errorf("messages must not be empty")
	}
	var head strings.Builder
	if p := strings.TrimSpace(s.cfg.Prompt.Preamble); p != "" {
		head.WriteString(p + "\n\n")
	}
	if s.cfg.Prompt.DiscourageTools {
		head.WriteString(web.ToolsInstruction + "\n\n")
	}
	if n := len(msgs); msgs[n-1].Role == "user" {
		lastUser = strings.TrimSpace(contentText(msgs[n-1].Content))
	}
	if s.cfg.Prompt.Format == "structured" {
		var system, turns []string
		for _, m := range msgs {
			t := strings.TrimSpace(contentText(m.Content))
			switch m.Role {
			case "system", "developer":
				if t != "" {
					system = append(system, t)
				}
			case "user":
				turns = append(turns, "User: "+t)
			case "assistant":
				turns = append(turns, "Assistant: "+t)
			case "tool":
				turns = append(turns, "Tool result: "+t)
			}
		}
		if len(system) > 0 {
			head.WriteString("Instructions:\n" + strings.Join(system, "\n\n") + "\n\n")
		}
		closing := "\n\nReply as the Assistant to the last User message. Output only the reply."
		full = head.String() + "Conversation so far:\n" + strings.Join(turns, "\n\n") + closing
		context = full
		if lastUser != "" && len(turns) > 1 {
			context = head.String() + "Conversation so far:\n" + strings.Join(turns[:len(turns)-1], "\n\n") + "\n\nThe next User message follows outside this text. Reply as the Assistant to it."
		}
		return full, context, lastUser, nil
	}
	var parts []string
	for _, m := range msgs {
		if t := strings.TrimSpace(contentText(m.Content)); t != "" {
			parts = append(parts, t)
		}
	}
	full = head.String() + strings.Join(parts, "\n\n")
	context = full
	if lastUser != "" && len(parts) > 1 {
		context = head.String() + strings.Join(parts[:len(parts)-1], "\n\n")
	}
	return full, context, lastUser, nil
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

type usage struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	Details          map[string]int `json:"completion_tokens_details,omitempty"`
}

func usageMap(u map[string]int, prompt, reply string) usage {
	in, out := u["input_tokens"], u["output_tokens"]
	if in == 0 {
		in = (utf8.RuneCountInString(prompt) + 3) / 4
	}
	if out == 0 {
		out = (utf8.RuneCountInString(reply) + 3) / 4
	}
	m := usage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}
	if t := u["thinking_tokens"]; t > 0 {
		m.Details = map[string]int{"reasoning_tokens": t}
	}
	return m
}

func (s *Server) handleChat(c *fiber.Ctx) error {
	var req chatRequest
	rawBody := append([]byte(nil), c.Body()...) // kept verbatim for the history file
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": "invalid JSON body: " + err.Error(), "type": "invalid_request_error"}})
	}
	full, promptContext, lastUser, err := s.buildPrompt(req.Messages)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": err.Error(), "type": "invalid_request_error"}})
	}
	s.recentMu.Lock()
	s.recent = append([]chatRequest{req}, s.recent...)
	if len(s.recent) > 5 {
		s.recent = s.recent[:5]
	}
	s.recentMu.Unlock()
	b, model, err := s.resolve(req.Model, c.Locals("all") == true)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": err.Error(), "type": "invalid_request_error"}})
	}
	id := newID()
	ctx, cancel := context.WithCancel(context.Background())
	s.st.SetCancel(id, cancel)
	// A body produced by a stream writer is written after this handler
	// returns, so those paths cancel at the end of the writer instead of here.
	bodyLater := req.Stream || s.cfg.Server.Keepalive > 0
	if !bodyLater {
		defer cancel()
	}
	var reasonMu sync.Mutex
	stopReason := "" // set when the client goes away, so the record says why
	clientGone := func() {
		reasonMu.Lock()
		stopReason = "client went away"
		reasonMu.Unlock()
		s.log.Printf("%s client went away; stopping", id)
		cancel()
	}
	startedAt := time.Now()
	created := startedAt.Unix()
	shown := req.Model
	if shown == "" {
		shown = b.Name() + "/default"
	}
	// agy silently cuts the middle out of messages above its cap; with
	// oversize_action = "web" such prompts go to the web backend instead,
	// which delivers them as a file attachment.
	rerouted := ""
	if act := s.cfg.Agy.OversizeAction; b.Name() == "agy" && len(full) > agy.MaxMessageBytes && (act == "web" || act == "agyapi") {
		if alt, ok := s.backends[act]; ok && alt.Ready() == nil {
			rerouted = fmt.Sprintf("prompt is %d bytes, above agy's %d-byte message cap; sent to the %s backend instead", len(full), agy.MaxMessageBytes, act)
			b, model = alt, ""
		}
	}
	s.st.Start(id, b.Name(), model, backend.Chars(full), req.Stream)
	if rerouted != "" {
		s.log.Printf("%s %s", id, rerouted)
		s.st.Update(id, state.Queued, 0, rerouted)
		s.st.SetFrom(id, "agy")
	}
	s.log.Printf("%s %s model=%q stream=%v prompt=%d chars", id, b.Name(), model, req.Stream, backend.Chars(full))
	c.Set("X-Openmini-Request-Id", id)

	body := func(res backend.Result, reply string) completion {
		return completion{ID: id, Object: "chat.completion", Created: created, Model: shown,
			Choices: []choice{{Index: 0, Message: &message{Role: "assistant", Content: reply}, FinishReason: "stop"}},
			Usage:   usageMap(res.Usage, full, reply)}
	}
	// the history file: written now with the request, rewritten when done
	histName := ""
	histMeta := history.Meta{}
	if s.hist != nil {
		histName = history.Name(startedAt, b.Name(), model, id)
		histMeta = history.Meta{"id": id, "backend": b.Name(), "model": model, "requested": req.Model, "stream": req.Stream,
			"started": startedAt.Format(time.RFC3339), "phase": "running"}
		if model == "" {
			histMeta["model"] = "default"
		}
		if rerouted != "" {
			histMeta["rerouted"] = rerouted
		}
		if err := s.hist.Write(histName, rawBody, nil, histMeta); err != nil {
			s.log.Printf("%s history: %v", id, err)
		}
	}

	call := backend.Call{ID: id, Ctx: ctx, Model: model, Prompt: full, Context: promptContext, LastUser: lastUser}
	finish := func(res backend.Result, err error, t0 time.Time) string {
		reply := res.Text
		phase := state.Finished
		note := ""
		if err != nil {
			phase = state.Failed
			note = err.Error()
			if reply != "" {
				reply += "\n"
			}
			reply += "[openmini/" + b.Name() + "] " + err.Error()
		}
		if note == "stopped by request" || strings.HasSuffix(note, ": stopped by request") {
			phase = state.Stopped
			reasonMu.Lock()
			if stopReason != "" {
				note = stopReason
			}
			reasonMu.Unlock()
		}
		s.st.Finish(id, phase, backend.Chars(reply), note)
		s.log.Printf("%s done in %.1fs phase=%s reply=%d chars usage=%v", id, time.Since(t0).Seconds(), phase, backend.Chars(reply), res.Usage)
		if s.hist != nil && histName != "" {
			now := time.Now()
			histMeta["finished"], histMeta["elapsed_s"], histMeta["phase"], histMeta["note"] = now.Format(time.RFC3339), now.Sub(startedAt).Seconds(), string(phase), note
			if err := s.hist.Write(histName, rawBody, body(res, reply), histMeta); err != nil {
				s.log.Printf("%s history: %v", id, err)
			}
		}
		return reply
	}

	if !req.Stream {
		t0 := time.Now()
		noteHeader := rerouted
		call.OnPhase = func(p, note string) {
			if p == "model" || p == "note" {
				noteHeader = note
				s.st.Update(id, state.Queued, 0, note)
				return
			}
			s.st.Update(id, state.Phase(p), 0, note)
		}
		call.OnText = func(soFar string) { s.st.Update(id, state.Generating, backend.Chars(soFar), "") }
		if ka := s.cfg.Server.Keepalive; ka > 0 {
			// Opt-in: a space every ka seconds while waiting is still valid
			// JSON, and the first failed write says the client is gone.
			c.Set("Content-Type", "application/json; charset=utf-8")
			c.Set("X-Accel-Buffering", "no")
			c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
				defer cancel()
				var wmu sync.Mutex
				dead := false
				done := make(chan struct{})
				go func() {
					t := time.NewTicker(time.Duration(ka) * time.Second)
					defer t.Stop()
					for {
						select {
						case <-done:
							return
						case <-t.C:
							wmu.Lock()
							if !dead {
								if _, err := w.WriteString(" "); err != nil || w.Flush() != nil {
									dead = true
									clientGone()
								}
							}
							wmu.Unlock()
						}
					}
				}()
				res, err := b.Complete(call)
				close(done)
				wmu.Lock()
				defer wmu.Unlock()
				reply := finish(res, err, t0)
				if dead {
					return
				}
				j, _ := json.Marshal(body(res, reply))
				w.Write(j)
				w.Flush()
			})
			return nil
		}
		res, err := b.Complete(call)
		reply := finish(res, err, t0)
		if noteHeader != "" {
			c.Set("X-Openmini-Note", noteHeader)
		}
		return c.JSON(body(res, reply))
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		var mu sync.Mutex
		var lastWrite time.Time
		dead := false
		write := func(line string) {
			mu.Lock()
			defer mu.Unlock()
			if dead {
				return
			}
			if _, err := w.WriteString(line); err != nil || w.Flush() != nil {
				// the client hung up: stop the backend instead of finishing for nobody
				dead = true
				clientGone()
				return
			}
			lastWrite = time.Now()
		}
		send := func(v any) {
			j, _ := json.Marshal(v)
			write("data: " + string(j) + "\n\n")
		}
		chunk := func(delta fiber.Map, finishReason any) completion {
			return completion{ID: id, Object: "chat.completion.chunk", Created: created, Model: shown,
				Choices: []chunkChoice{{Index: 0, Delta: delta, FinishReason: finishReason}}}
		}
		// first chunk acknowledges the request; phases follow as SSE comments
		send(chunk(fiber.Map{"role": "assistant", "content": ""}, nil))
		write(": openmini phase=queued id=" + id + "\n\n")
		if rerouted != "" {
			write(": openmini note=" + rerouted + "\n\n")
		}
		sent := ""
		t0 := time.Now()
		// keep-alive comments every 5s while nothing else is written: clients
		// and phones do not drop a slow but healthy request, and a client that
		// hung up is noticed within a couple of writes
		done := make(chan struct{})
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if time.Since(lastWrite) >= 4*time.Second {
						write(": openmini keepalive\n\n")
					}
				}
			}
		}()
		defer close(done)
		call.OnPhase = func(p, note string) {
			if p == "model" || p == "note" {
				s.st.Update(id, state.Queued, 0, note)
				write(": openmini note=" + strings.ReplaceAll(note, "\n", " ") + "\n\n")
				return
			}
			s.st.Update(id, state.Phase(p), 0, note)
			write(": openmini phase=" + p + "\n\n")
		}
		call.OnText = func(soFar string) {
			s.st.Update(id, state.Generating, backend.Chars(soFar), "")
			stable := soFar
			if !b.StableStream() {
				// partial rendered text can still change shape; only send up to the last blank line
				cut := strings.LastIndex(soFar, "\n\n")
				if cut < 0 {
					return
				}
				stable = soFar[:cut+2]
			}
			if len(stable) > len(sent) && strings.HasPrefix(stable, sent) {
				send(chunk(fiber.Map{"content": stable[len(sent):]}, nil))
				sent = stable
			}
		}
		res, err := b.Complete(call)
		final := finish(res, err, t0)
		if rest := remainder(sent, final); rest != "" {
			send(chunk(fiber.Map{"content": rest}, nil))
		}
		send(chunk(fiber.Map{}, "stop"))
		write("data: [DONE]\n\n")
	})
	return nil
}

// handlePolicyBisect finds which fragments of a prompt trip the service's
// pre-submission blocklist. It takes a normal chat body, sends the full prompt
// to the chosen backend, and if that comes back as the policy notice, splits
// the text and re-tests pieces until the blocked fragments are small.
// Probes return in well under a second and consume no quota.
func (s *Server) handlePolicyBisect(c *fiber.Ctx) error {
	var req chatRequest
	if c.Query("last") != "" {
		// bisect the most recent chat request the server received (kept in memory only)
		s.recentMu.Lock()
		if len(s.recent) > 0 {
			req = s.recent[0]
		}
		s.recentMu.Unlock()
		if len(req.Messages) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "no request seen since start; send the request once, then call this with ?last=1"})
		}
		if m := c.Query("model"); m != "" {
			req.Model = m
		}
	} else if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	full, _, _, err := s.buildPrompt(req.Messages)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	b, model, err := s.resolve(req.Model, true) // the tool takes any id a backend has
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	// Probes must be cheap: one output token, so a fragment that passes the
	// blocklist returns at once instead of generating a reply.
	if c.Query("last") != "" && c.Query("model") == "" && b.Name() == "agyapi" {
		model = s.probeModel(b)
	}
	blocked := func(text string) (bool, string) {
		res, err := b.Complete(backend.Call{ID: "bisect", Model: model, Prompt: text, MaxTokens: 1})
		if err != nil {
			return false, err.Error()
		}
		t := strings.TrimSpace(res.Text)
		hit := strings.Contains(t, "Prohibited Use") || strings.Contains(t, "could not be submitted") || strings.Contains(t, "sensitive words")
		return hit, t
	}
	isBlocked, first := blocked(full)
	if !isBlocked {
		return c.JSON(fiber.Map{"blocked": false, "reply_start": first[:min(len(first), 120)], "probes": 1})
	}
	// Recursive split on paragraph and line boundaries; keep pieces that are
	// still blocked; stop when a piece is small enough to read.
	const minChars = 160
	probes := 1
	var found []string
	var dig func(text string, depth int)
	dig = func(text string, depth int) {
		if backend.Chars(text) <= minChars || depth > 12 {
			found = append(found, text)
			return
		}
		parts := splitHalf(text)
		anyBlocked := false
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			probes++
			if hit, _ := blocked(part); hit {
				anyBlocked = true
				dig(part, depth+1)
			}
		}
		if !anyBlocked {
			// the halves pass alone; the trigger spans the cut, keep the whole piece
			found = append(found, text)
		}
	}
	dig(full, 0)
	return c.JSON(fiber.Map{"blocked": true, "probes": probes, "fragments": found, "notice": first})
}

// splitHalf cuts text near its middle, preferring a blank line, then a line break.
func splitHalf(text string) []string {
	mid := len(text) / 2
	cut := -1
	for _, sep := range []string{"\n\n", "\n", "。", ". ", " "} {
		if i := strings.LastIndex(text[:mid], sep); i > len(text)/6 {
			cut = i + len(sep)
			break
		}
		if i := strings.Index(text[mid:], sep); i >= 0 && mid+i < len(text)*5/6 {
			cut = mid + i + len(sep)
			break
		}
	}
	if cut <= 0 || cut >= len(text) {
		cut = mid
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
	}
	return []string{text[:cut], text[cut:]}
}

func remainder(sent, final string) string {
	if strings.HasPrefix(final, sent) {
		return final[len(sent):]
	}
	i := 0
	for i < len(sent) && i < len(final) && sent[i] == final[i] {
		i++
	}
	for i > 0 && i < len(final) && !utf8.RuneStart(final[i]) {
		i--
	}
	return final[i:]
}
