package publish_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hec-ovi/censurado-web-backend/internal/publish"
)

// countingTrigger is a fake regenTrigger that counts how many times it fired.
type countingTrigger struct{ n int32 }

func (c *countingTrigger) Trigger()   { atomic.AddInt32(&c.n, 1) }
func (c *countingTrigger) count() int { return int(atomic.LoadInt32(&c.n)) }

// Each settled trigger runs the pass exactly once.
func TestRegenerator_RunsOncePerTrigger(t *testing.T) {
	var runs int32
	done := make(chan struct{}, 8)
	rg := publish.NewRegenerator(func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		done <- struct{}{}
		return nil
	}, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rg.Start(ctx)

	rg.Trigger()
	waitRun(t, done)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs = %d, want 1 after one trigger", got)
	}

	rg.Trigger()
	waitRun(t, done)
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("runs = %d, want 2 after a second trigger", got)
	}
}

// A burst of triggers within the debounce window collapses to exactly one run.
func TestRegenerator_BurstCoalescesToOneRun(t *testing.T) {
	var runs int32
	done := make(chan struct{}, 64)
	rg := publish.NewRegenerator(func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		done <- struct{}{}
		return nil
	}, 80*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rg.Start(ctx)

	// Fire a tight burst, well inside the 80ms window.
	for i := 0; i < 25; i++ {
		rg.Trigger()
	}
	waitRun(t, done)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs = %d after a burst, want 1 (coalesced)", got)
	}
	// No second run should follow: the burst is fully absorbed.
	select {
	case <-done:
		t.Fatalf("unexpected second run; burst did not coalesce")
	case <-time.After(250 * time.Millisecond):
	}
}

// Trigger never blocks, even when the run is stuck and the buffer is full.
func TestRegenerator_TriggerNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	rg := publish.NewRegenerator(func(context.Context) error {
		<-block // never released during the test
		return nil
	}, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rg.Start(ctx)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			rg.Trigger()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger blocked; it must be non-blocking")
	}
	close(block)
}

// A publish returns promptly even while the regenerate run is blocked: the run is
// off the request path, and the worker still picks up the trigger.
func TestHandler_RegenIsOffRequestPath(t *testing.T) {
	h, _ := newHandler(t)
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	rg := publish.NewRegenerator(func(context.Context) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return nil
	}, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rg.Start(ctx)
	h.WithRegenerator(rg)
	srv := publish.NewServerHandler(h, nil, nil, nil, nil)

	resp := make(chan int, 1)
	go func() { resp <- post(t, srv, "ak_ada."+adaSecret, "regen-1", validBody).Code }()
	select {
	case code := <-resp:
		if code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not return; regenerate is blocking the request path")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("regenerate worker never started after the publish")
	}
	close(block)
}

// Only a publish that creates new content triggers a regenerate; replays and
// content-hash dedup (single and batch) change nothing on disk and trigger nothing.
func TestHandler_TriggersRegenOnCreateOnly(t *testing.T) {
	h, _ := newHandler(t)
	fake := &countingTrigger{}
	h.WithRegenerator(fake)
	srv := publish.NewServerHandler(h, nil, nil, nil, nil)
	tok := "ak_ada." + adaSecret

	post(t, srv, tok, "c1", validBody) // create -> trigger
	post(t, srv, tok, "c1", validBody) // idempotent replay -> no trigger
	post(t, srv, tok, "c2", validBody) // content-hash dedup -> no trigger
	if got := fake.count(); got != 1 {
		t.Fatalf("triggers = %d, want 1 (only the create)", got)
	}

	batch := batchBody(t, []bItem{{Title: "Fresh batch story", Body: "b", Author: "ada", Section: "tech", Key: "rb1"}})
	postBatch(t, srv, tok, batch) // batch create -> trigger
	if got := fake.count(); got != 2 {
		t.Fatalf("triggers = %d, want 2 after a batch create", got)
	}
	postBatch(t, srv, tok, batch) // batch replay -> no trigger
	if got := fake.count(); got != 2 {
		t.Fatalf("triggers = %d, want still 2 after a batch replay", got)
	}
}

func waitRun(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("regenerate run did not happen within the timeout")
	}
}
