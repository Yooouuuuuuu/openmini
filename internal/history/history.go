// Package history keeps one JSON file per request when it is switched on:
// the client's body as received, the completion object as returned, and
// openmini's own notes about the request, each under its own key so the two
// bodies stay exactly what went over the wire.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct{ dir string }

// Meta is what openmini records about a request: id, backend, model,
// timings, phase, note.
type Meta map[string]any

type file struct {
	Request  json.RawMessage `json:"request"`
	Response any             `json:"response"`
	Openmini Meta            `json:"openmini"`
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// Name is the file name for a request: timestamp, backend, model and the
// tail of the request id, so two requests starting in the same second never
// share a name.
func Name(started time.Time, backend, model, id string) string {
	if model == "" {
		model = "default"
	}
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return fmt.Sprintf("%s_%s_%s_%s.json", started.Format("20060102-150405"), safe(backend), safe(model), id)
}

// safe keeps a name usable on every filesystem: separators, reserved
// characters and spaces become dashes.
func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ', '\t', '\n':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Write stores the file, replacing any earlier version, through a temporary
// name so a reader never sees a half-written file.
func (s *Store) Write(name string, request []byte, response any, meta Meta) error {
	raw := json.RawMessage(request)
	if !json.Valid(request) {
		raw, _ = json.Marshal(string(request)) // not JSON: keep it as a string
	}
	out, err := json.MarshalIndent(file{Request: raw, Response: response, Openmini: meta}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
