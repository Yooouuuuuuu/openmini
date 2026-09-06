// Package config loads openmini's TOML configuration and carries the template
// that `openmini init` and run.sh write when no config exists yet.
package config

import (
    "fmt"
    "os"

    "github.com/BurntSushi/toml"
)

type Config struct {
    Server Server `toml:"server"`
    Log    Log    `toml:"log"`
    Prompt Prompt `toml:"prompt"`
    Web    Web    `toml:"web"`
    Agy    Agy    `toml:"agy"`
    AgyAPI AgyAPI `toml:"agyapi"`
}

type Server struct {
    Port           int      `toml:"port"`
    DefaultBackend string   `toml:"default_backend"` // backend used when the model name carries no prefix
    APIKeys        []string `toml:"api_keys"`        // when non-empty, requests must carry one as a Bearer token
    Timeout        int      `toml:"timeout"`         // seconds to wait for a complete reply; 0 = no limit
}

type Log struct {
    Directory string `toml:"directory"`
    KeepDays  int    `toml:"keep_days"`
}

type Prompt struct {
    Format          string `toml:"format"`           // "direct" or "structured"
    Preamble        string `toml:"preamble"`         // text placed before every conversation
    DiscourageTools bool   `toml:"discourage_tools"` // ask for direct answers without search/code/image tools
}

type Web struct {
    Enabled        bool   `toml:"enabled"`
    Headless       bool   `toml:"headless"`
    ProfileDir     string `toml:"profile_dir"`
    Model          string `toml:"model"`            // picker entry, e.g. "3.1 Pro"; empty leaves the picker alone
    InputMethod    string `toml:"input_method"`     // "paste" or "insert"
    ReplySource    string `toml:"reply_source"`     // "copy" or "html"
    LongPromptMode string `toml:"long_prompt_mode"` // "paste", "attach", "split"
    MaxInlineChars int    `toml:"max_inline_chars"`
    AttachName     string `toml:"attach_name"`
    AttachFillBox  bool   `toml:"attach_fill_box"`
    StartTimeout   int    `toml:"start_timeout"` // seconds to wait for Gemini to open a reply; 0 = no limit
    Retries        int    `toml:"retries"`       // extra attempts when Gemini answers with an error notice or refusal
}

type Agy struct {
    Enabled   bool   `toml:"enabled"`
    Binary    string `toml:"binary"`
    Agent     string `toml:"agent"`
    Mode      string `toml:"mode"`
    Model     string `toml:"model"`
    Effort    string `toml:"effort"`
    Workspace string `toml:"workspace"`
    Parallel  int    `toml:"parallel"`
    // "agyapi" or "web": hand oversized prompts to that backend; "warn": send
    // them to agy anyway and report the dropped tail; "fail": refuse them.
    OversizeAction string `toml:"oversize_action"`
}

type AgyAPI struct {
    Enabled   bool     `toml:"enabled"`
    TokenFile string   `toml:"token_file"` // agy's stored OAuth token
    AgyBinary string   `toml:"agy_binary"` // only used to refresh the token
    UserAgent string   `toml:"user_agent"`
    IDEType   string   `toml:"ide_type"`
    Endpoint  string   `toml:"endpoint"`
    Project   string   `toml:"project"` // override; empty = ask loadCodeAssist
    Model     string   `toml:"model"`   // default model id, agy-style suffix allowed
    Models    []string `toml:"models"`  // fallback list when the service lists none
    CreditTypes []string `toml:"credit_types"` // subscription entitlements to use, e.g. ["GOOGLE_ONE_AI"]
    OfficialPrompt    bool   `toml:"official_prompt"`    // send the Antigravity identity snippet as the first system part
    SystemInstruction string `toml:"system_instruction"` // optional instruction of your own, sent after it
    ProbeModel        string `toml:"probe_model"`        // cheap model used by /tools/policy-bisect
}

// Load reads path and fills in defaults for anything left out.
func Load(path string) (*Config, error) {
    var c Config
    if _, err := toml.DecodeFile(path, &c); err != nil {
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    c.applyDefaults()
    return &c, nil
}

func (c *Config) applyDefaults() {
    if c.Server.Port == 0 {
        c.Server.Port = 18000
    }
    if c.Server.DefaultBackend == "" {
        c.Server.DefaultBackend = "web"
    }
    if c.Log.Directory == "" {
        c.Log.Directory = "./logs"
    }
    if c.Log.KeepDays <= 0 {
        c.Log.KeepDays = 7
    }
    if c.Prompt.Format == "" {
        c.Prompt.Format = "direct"
    }
    w := &c.Web
    if w.ProfileDir == "" {
        w.ProfileDir = "./data/browser-profile"
    }
    if w.InputMethod == "" {
        w.InputMethod = "paste"
    }
    if w.ReplySource == "" {
        w.ReplySource = "copy"
    }
    if w.LongPromptMode == "" {
        w.LongPromptMode = "paste"
    }
    if w.MaxInlineChars <= 0 {
        w.MaxInlineChars = 30000
    }
    if w.AttachName == "" {
        w.AttachName = "prompt.txt"
    }
    a := &c.Agy
    if a.Binary == "" {
        a.Binary = "agy"
    }
    x := &c.AgyAPI
    if x.TokenFile == "" {
        x.TokenFile = "~/.gemini/antigravity-cli/antigravity-oauth-token"
    }
    if x.AgyBinary == "" {
        x.AgyBinary = a.Binary
    }
    if x.UserAgent == "" {
        x.UserAgent = "antigravity/1.13.0 linux/amd64"
    }
    if x.IDEType == "" {
        x.IDEType = "ANTIGRAVITY"
    }
    if x.Endpoint == "" {
        x.Endpoint = "https://daily-cloudcode-pa.googleapis.com"
    }
    if x.Model == "" {
        x.Model = "gemini-3.1-pro-high"
    }
    if x.ProbeModel == "" {
        x.ProbeModel = "gemini-3.5-flash-low"
    }
    if x.CreditTypes == nil {
        x.CreditTypes = []string{"GOOGLE_ONE_AI"}
    }
    if len(x.Models) == 0 {
        x.Models = []string{"gemini-3.1-pro-high", "gemini-3.1-pro-low", "gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-3.5-flash-lite"}
    }
    if a.Workspace == "" {
        a.Workspace = "./data/agy-workspace"
    }
    if a.Parallel <= 0 {
        a.Parallel = 1
    }
}

// WriteTemplate writes the example configuration to path.
func WriteTemplate(path string) error {
    return os.WriteFile(path, []byte(Template), 0644)
}

// Template is config.example.toml. Keep it in sync with the fields above.
const Template = `# openmini configuration. Copy to config.toml and edit; config.toml is not committed.

[server]
port = 18000
# Backend used when the model name has no "web/" or "agy/" prefix.
default_backend = "web"
# Optional. When set, requests must send "Authorization: Bearer <key>". Leave
# empty for localhost or tailnet-only use.
api_keys = []
# Seconds to wait for a complete reply once it has started; 0 = no limit.
timeout = 0

[log]
directory = "./logs"
# One file per day; older files are deleted. The log never contains prompt or
# reply text, only ids, sizes, timings and status.
keep_days = 7

[prompt]
# "direct": message contents are sent in order, unchanged, joined by blank
# lines. "structured": role labels and an Instructions block are added.
format = "direct"
# Optional text placed before every conversation.
preamble = ""
# Ask the model to answer directly without search, code execution, images or
# tool calls (prompt text only; not enforced).
discourage_tools = false

[web]
# Drives gemini.google.com in a browser with a persistent, signed-in profile.
enabled = true
headless = true
profile_dir = "./data/browser-profile"
# Model picker entry to select, e.g. "3.1 Pro", "3.8 Flash". Empty leaves it.
model = "3.1 Pro"
# "paste" carries long multi-line prompts intact; "insert" is capped at ~32k by the page.
input_method = "paste"
# "copy" reads the reply from Gemini's copy button (exact Markdown); "html" converts the rendered reply.
reply_source = "copy"
# Prompts up to max_inline_chars are pasted (verified intact up to ~100k characters);
# longer ones use long_prompt_mode: "attach" (upload as a text file, verified at 104k),
# "split" (several messages), or "paste" (send anyway; the backend drops very long ones).
long_prompt_mode = "attach"
max_inline_chars = 100000
attach_name = "prompt.txt"
attach_fill_box = false
# Seconds to wait for Gemini to open a reply after submitting; 0 = no limit.
start_timeout = 180
# Extra attempts when Gemini answers with an error notice or refusal; 0 passes everything through.
retries = 0

[agy]
# Runs the Antigravity CLI (agy) with a tool-less custom agent.
enabled = true
binary = "~/.local/bin/agy"
agent = "storyteller"
mode = "plan"
model = "gemini-3.1-pro-low"
effort = ""
workspace = "./data/agy-workspace"
parallel = 1
# agy silently drops everything after the first 192,000 bytes of a message (about 100k Chinese or 190k English characters). "web" hands such
# prompts to the web backend (file attachment) with a note; "warn" sends them to
# agy anyway and reports the cut; "fail" refuses them.
oversize_action = "web"

[agyapi]
# Talks to the Antigravity backend service directly with agy's stored token:
# no agent harness, so no 192,000-byte cap, no tool schemas, no system prompt
# overhead, real streaming, and per-model quota. Sign in with agy once; the
# token is refreshed by running agy when it nears expiry.
enabled = true
token_file = "~/.gemini/antigravity-cli/antigravity-oauth-token"
agy_binary = "~/.local/bin/agy"
user_agent = "antigravity/1.13.0 linux/amd64"
ide_type = "ANTIGRAVITY"
# The CLI's token is entitled on the daily host (what agy itself calls); the
# production host cloudcode-pa.googleapis.com answers 429 for generation.
endpoint = "https://daily-cloudcode-pa.googleapis.com"
# project = ""    # normally resolved automatically
# Default model id, as listed by the service (agy's slugs, e.g. gemini-3.1-pro-low).
model = "gemini-3.1-pro-low"
# Subscription entitlement the requests draw on. GOOGLE_ONE_AI is the Google AI
# Pro/Ultra plan; set to [] to send none.
credit_types = ["GOOGLE_ONE_AI"]
# Send the opening of Antigravity's own system prompt (wrapped as a
# non-instruction) as the first system part, so requests look like the IDE's.
official_prompt = true
# Optional instruction of your own, sent after it as system text.
system_instruction = ""
`
