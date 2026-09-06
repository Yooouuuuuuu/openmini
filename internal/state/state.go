// Package state tracks what every request is doing right now, so a client can
// tell a slow answer from a stuck one.
package state

import (
    "sync"
    "time"
)

type Phase string

const (
    Queued     Phase = "queued"     // waiting for the backend to be free
    Submitted  Phase = "submitted"  // prompt delivered, waiting for the reply to start
    Generating Phase = "generating"
    Thinking Phase = "thinking"
    Stopped Phase = "stopped" // reply is being produced
    Finished   Phase = "finished"
    Failed     Phase = "failed"
)

type Request struct {
    ID          string    `json:"id"`
    Backend     string    `json:"backend"`
    Model       string    `json:"model"`
    Stream      bool      `json:"stream"`
    Phase       Phase     `json:"phase"`
    Started     time.Time `json:"started"`
    Updated     time.Time `json:"updated"`
    ElapsedSec  float64   `json:"elapsed_s"`
    PromptChars int       `json:"prompt_chars"`
    ReplyChars  int       `json:"reply_chars"`
    Note        string    `json:"note,omitempty"`
}

type Registry struct {
    mu      sync.Mutex
    active  map[string]*Request
    recent  []*Request
    cancels map[string]func()
    subs    map[chan struct{}]struct{}
}

func New() *Registry {
    return &Registry{active: map[string]*Request{}, cancels: map[string]func(){}, subs: map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that gets a tick whenever any request changes,
// and a function that ends the subscription. Ticks coalesce: a slow reader
// sees one tick for a burst of changes.
func (r *Registry) Subscribe() (<-chan struct{}, func()) {
    ch := make(chan struct{}, 1)
    r.mu.Lock()
    r.subs[ch] = struct{}{}
    r.mu.Unlock()
    return ch, func() {
        r.mu.Lock()
        delete(r.subs, ch)
        r.mu.Unlock()
    }
}

// notify wakes every subscriber without blocking; the caller holds r.mu.
func (r *Registry) notify() {
    for ch := range r.subs {
        select {
        case ch <- struct{}{}:
        default:
        }
    }
}

// SetCancel stores the function that stops request id.
func (r *Registry) SetCancel(id string, fn func()) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.cancels[id] = fn
}

// Cancel stops the active request id and reports whether it was found.
func (r *Registry) Cancel(id string) bool {
    r.mu.Lock()
    fn, ok := r.cancels[id]
    r.mu.Unlock()
    if ok && fn != nil {
        fn()
    }
    return ok
}

func (r *Registry) Start(id, backend, model string, promptChars int, stream bool) {
    r.mu.Lock()
    defer r.mu.Unlock()
    defer r.notify() // runs before the unlock above
    now := time.Now()
    r.active[id] = &Request{ID: id, Backend: backend, Model: model, Stream: stream, Phase: Queued, Started: now, Updated: now, PromptChars: promptChars}
}

func (r *Registry) Update(id string, phase Phase, replyChars int, note string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    defer r.notify() // runs before the unlock above
    if q, ok := r.active[id]; ok {
        q.Phase, q.Updated = phase, time.Now()
        if replyChars > 0 {
            q.ReplyChars = replyChars
        }
        if note != "" {
            q.Note = note
        }
    }
}

func (r *Registry) Finish(id string, phase Phase, replyChars int, note string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    defer r.notify() // runs before the unlock above
    q, ok := r.active[id]
    if !ok {
        return
    }
    delete(r.active, id)
    delete(r.cancels, id)
    q.Phase, q.Updated, q.ReplyChars, q.Note = phase, time.Now(), replyChars, note
    q.ElapsedSec = q.Updated.Sub(q.Started).Seconds()
    r.recent = append([]*Request{q}, r.recent...)
    if len(r.recent) > 50 {
        r.recent = r.recent[:50]
    }
}

// Snapshot returns copies of the active and recent requests.
func (r *Registry) Snapshot() (active []Request, recent []Request) {
    r.mu.Lock()
    defer r.mu.Unlock()
    now := time.Now()
    for _, q := range r.active {
        c := *q
        c.ElapsedSec = now.Sub(c.Started).Seconds()
        active = append(active, c)
    }
    for _, q := range r.recent {
        recent = append(recent, *q)
    }
    return
}
