// Package backend defines what a text backend must provide. The API layer
// builds prompts, tracks state and speaks OpenAI; backends only deliver text.
package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// Accepter is implemented by a backend that serves ids beyond the ones it
// lists, such as names the service has renamed. The API layer asks it
// before handing a request over from another backend.
type Accepter interface {
	Accepts(id string) bool
}

// Chars counts characters, the unit page limits are measured in.
func Chars(s string) int { return utf8.RuneCountInString(s) }
