package web

import (
	"fmt"

	"openmini/internal/backend"
)

// Pool runs several chat tabs (lanes) in the one browser and hands each
// request to a free one, so requests overlap instead of queueing behind a
// single tab. Lane 0 is the Web the browser was started with; it also
// answers the usage, model and debug questions.
type Pool struct {
	lanes []*Web
	free  chan *Web
}

// NewPool opens n-1 more tabs next to first and returns the pool.
func NewPool(first *Web, n int) (*Pool, error) {
	p := &Pool{lanes: []*Web{first}, free: make(chan *Web, n)}
	p.free <- first
	for i := 1; i < n; i++ {
		l, err := first.NewLane(i)
		if err != nil {
			return nil, fmt.Errorf("lane %d: %v", i, err)
		}
		p.lanes = append(p.lanes, l)
		p.free <- l
	}
	return p, nil
}

func (p *Pool) Name() string        { return "web" }
func (p *Pool) StableStream() bool  { return false }
func (p *Pool) Models() []string    { return p.lanes[0].Models() }
func (p *Pool) Ready() error        { return p.lanes[0].Ready() }
func (p *Pool) Usage() (any, error) { return p.lanes[0].Usage() }
func (p *Pool) Lanes() int          { return len(p.lanes) }

// Complete waits for a free lane (the request shows as queued meanwhile) and
// runs the call on it.
func (p *Pool) Complete(c backend.Call) (backend.Result, error) {
	var l *Web
	select {
	case l = <-p.free:
	case <-c.Ctx.Done():
		return backend.Result{}, fmt.Errorf("stopped by request")
	}
	defer func() { p.free <- l }()
	return l.Complete(c)
}
