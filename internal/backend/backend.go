// Package backend defines what a text backend must provide. The API layer
// builds prompts, tracks state and speaks OpenAI; backends only deliver text.
package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"openmini/internal/proc"
)

// FindAgy returns the agy binary to run: bin (with a leading ~ expanded) if
// it exists or is on the PATH, else the folder Google's installer uses on this
// OS (a shell opened before the install does not see the new PATH yet).
func FindAgy(bin string) string {
	if bin == "" {
		bin = "agy"
	}
	if strings.HasPrefix(bin, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			bin = filepath.Join(home, bin[2:])
		}
	}
	if _, err := exec.LookPath(bin); err == nil {
		return bin
	}
	var p string
	if runtime.GOOS == "windows" {
		p = filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin", "agy.exe")
	} else if home, err := os.UserHomeDir(); err == nil {
		p = filepath.Join(home, ".local", "bin", "agy")
	}
	if p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return bin
}

// PrepareAgy readies a command that runs agy: no console window of its own,
// and agy's auto-updater off. Without the latter every agy run spawns a
// separate update process, which on Windows opens a console window for a
// moment even when agy itself is hidden. The variable is agy's own; it
// only takes "true".
func PrepareAgy(cmd *exec.Cmd) {
	proc.Quiet(cmd)
	cmd.Env = append(os.Environ(), "AGY_CLI_DISABLE_AUTO_UPDATE=true")
}

// QuotaRow is one line of a quota meter: a pool (model group), its window,
// what is left, and when it resets.
type QuotaRow struct {
	Pool      string `json:"pool"`
	Window    string `json:"window"`
	Remaining string `json:"remaining"`
	ResetsAt  string `json:"resets_at,omitempty"`
	ResetsIn  string `json:"resets_in,omitempty"`
}

// AgyUsage runs agy's /usage and parses its tab-separated lines. agy and
// agyapi share one Antigravity quota, and the CLI's view is the complete one:
// the service's quota summary leaves model groups out for other clients.
func AgyUsage(bin, dir string) ([]QuotaRow, error) {
	cmd := exec.Command(bin, "--print", "/usage", "--output-format", "text")
	PrepareAgy(cmd)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var rows []QuotaRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 {
			continue
		}
		r := QuotaRow{Pool: f[0], Window: f[1], Remaining: f[2]}
		if len(f) >= 4 {
			r.ResetsAt = f[3]
			if t, err := time.Parse(time.RFC3339, f[3]); err == nil {
				r.ResetsIn = time.Until(t).Round(time.Minute).String()
			}
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return nil, fmt.Errorf("unexpected /usage output: %s", first)
	}
	return rows, nil
}

// Call is one prompt for a backend.
type Call struct {
	ID       string
	Ctx      context.Context // cancelled to stop the request; may be nil
	Model    string          // backend-specific model id; empty = backend default
	Prompt   string          // everything, in one string
	Context  string          // everything except the final user message (for attach/split modes)
	LastUser string          // the final user message when the last message was a user turn
	// MaxTokens caps the reply when > 0 (used for cheap probes). Backends
	// that cannot honour it ignore it.
	MaxTokens int
	// Transport is a reply function the client itself declared (a single
	// tool with one string argument, as anti-truncation presets send). A
	// backend using the tool transport declares this one instead of its
	// own, so the model sees one consistent instruction. Nil when none.
	Transport *Transport
	// OnText receives the reply text so far (cumulative), as it streams. May be nil.
	OnText func(soFar string)
	// OnPhase reports "submitted" and "generating" with an optional note. May be nil.
	OnPhase func(phase, note string)
}

// Result is a backend's answer. Text is delivered even when Status is not a
// success, so the client sees whatever the backend said.
type Result struct {
	Text   string
	Status string
	Usage  map[string]int
}

type Backend interface {
	Name() string
	// Ready reports whether the backend can serve (signed in, binary found).
	Ready() error
	// Models lists the ids this backend accepts, without any prefix.
	Models() []string
	// StableStream is true when OnText grows only by appending (safe to stream
	// immediately); false when partial text can still change shape.
	StableStream() bool
	Complete(c Call) (Result, error)
	// Usage returns backend-specific quota information, or an explanation.
	Usage() (any, error)
}

// Transport describes the reply function of the tool transport: the model
// is made to answer by calling it, and the value of Param is the reply.
type Transport struct {
	Name        string
	Description string
	Param       string
}

// Accepter is implemented by a backend that serves ids beyond the ones it
// lists, such as names the service has renamed. The API layer asks it
// before handing a request over from another backend.
type Accepter interface {
	Accepts(id string) bool
}

// Chars counts characters, the unit page limits are measured in.
func Chars(s string) int { return utf8.RuneCountInString(s) }
