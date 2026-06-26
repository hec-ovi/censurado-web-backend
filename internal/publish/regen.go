package publish

import (
	"context"
	"log"
	"time"
)

// defaultRegenDebounce is the settle window used when none is configured. It is
// long enough to collapse an agentic batch followed by a breaking-news single
// publish into one rebuild, short enough that readers see new content quickly.
const defaultRegenDebounce = 2 * time.Second

// Regenerator runs an incremental regenerate plus purge off the request path, on a
// debounce so a burst of publishes collapses to a single rebuild. The publish
// handler calls Trigger after a successful write; Trigger never blocks the request,
// and the run executes in the worker goroutine, so the response returns fast. The
// worker waits for the debounce window to settle after the most recent trigger,
// then runs one pass. A publish that arrives during a run is not lost: it schedules
// exactly one more pass afterward.
type Regenerator struct {
	run      func(ctx context.Context) error
	debounce time.Duration
	logf     func(format string, args ...any)
	triggers chan struct{}
}

// NewRegenerator wires a regenerator around run, the single regenerate-plus-purge
// pass. A non-positive debounce defaults to defaultRegenDebounce.
func NewRegenerator(run func(ctx context.Context) error, debounce time.Duration) *Regenerator {
	if debounce <= 0 {
		debounce = defaultRegenDebounce
	}
	return &Regenerator{
		run:      run,
		debounce: debounce,
		triggers: make(chan struct{}, 1), // capacity 1: pending triggers coalesce
	}
}

// WithLogger sets where regenerate failures are logged. Defaults to log.Printf.
func (r *Regenerator) WithLogger(logf func(format string, args ...any)) *Regenerator {
	r.logf = logf
	return r
}

func (r *Regenerator) warnf(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Trigger schedules a regenerate. It never blocks: if a regenerate is already
// pending it coalesces into it.
func (r *Regenerator) Trigger() {
	select {
	case r.triggers <- struct{}{}:
	default:
	}
}

// Start launches the worker, running until ctx is cancelled. Call it once.
func (r *Regenerator) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Regenerator) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.triggers:
			if !r.settle(ctx) {
				return
			}
			if err := r.run(ctx); err != nil {
				r.warnf("publish: regenerate failed: %v", err)
			}
		}
	}
}

// settle waits for the debounce window after the most recent trigger, absorbing
// any further triggers that arrive during it so a burst collapses to one run. It
// returns false if ctx was cancelled while waiting.
func (r *Regenerator) settle(ctx context.Context) bool {
	timer := time.NewTimer(r.debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-r.triggers:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(r.debounce)
		case <-timer.C:
			return true
		}
	}
}
