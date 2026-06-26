package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// blockingHandler signals on arrived when a request enters, then blocks until
// release is closed before replying 200 with body. It lets a test hold a request
// "in flight" across a shutdown.
func blockingHandler(arrived chan<- struct{}, release <-chan struct{}, body string) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(arrived) })
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
}

type clientResult struct {
	status int
	body   string
	err    error
}

// goGet fires GET addr in a goroutine and reports the outcome on the returned chan.
func goGet(addr string) <-chan clientResult {
	out := make(chan clientResult, 1)
	go func() {
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Get("http://" + addr + "/")
		if err != nil {
			out <- clientResult{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		out <- clientResult{status: resp.StatusCode, body: string(b)}
	}()
	return out
}

// TestServe_GracefulShutdownDrainsInflight asserts that cancelling the context (a
// SIGTERM stand-in) triggers Shutdown, lets an in-flight request finish, and returns
// exitOK. This is the write path's drain-not-drop guarantee.
func TestServe_GracefulShutdownDrainsInflight(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: blockingHandler(arrived, release, "drained")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- serve(ctx, func() {}, srv, func() error { return srv.Serve(ln) }, 5*time.Second, &stderr)
	}()

	resultCh := goGet(ln.Addr().String())

	<-arrived      // the request is now executing inside the handler
	cancel()       // simulate the signal: Shutdown begins with the request in flight
	close(release) // allow the in-flight request to complete

	if got := <-exitCh; got != exitOK {
		t.Fatalf("serve exit = %d, want exitOK (%d); stderr=%q", got, exitOK, stderr.String())
	}
	res := <-resultCh
	if res.err != nil {
		t.Fatalf("in-flight request failed instead of draining: %v", res.err)
	}
	if res.status != http.StatusOK || res.body != "drained" {
		t.Errorf("in-flight response = %d %q, want 200 \"drained\"", res.status, res.body)
	}
}

// TestServe_ShutdownErrorReturnsExitServe asserts that when Shutdown cannot drain
// in-flight work within the bounded window, serve reports the error and returns
// exitServe (the distinct serve-failure exit code).
func TestServe_ShutdownErrorReturnsExitServe(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: blockingHandler(arrived, release, "late")}
	t.Cleanup(func() { _ = srv.Close() }) // force-close any conn left after the timeout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	exitCh := make(chan int, 1)
	go func() {
		// 1ms drain window: the still-blocked request cannot finish in time.
		exitCh <- serve(ctx, func() {}, srv, func() error { return srv.Serve(ln) }, time.Millisecond, &stderr)
	}()

	resultCh := goGet(ln.Addr().String())
	<-arrived // request is in flight and will stay blocked through the drain window
	cancel()

	if got := <-exitCh; got != exitServe {
		t.Fatalf("serve exit = %d, want exitServe (%d) on a Shutdown timeout", got, exitServe)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("shutdown:")) {
		t.Errorf("stderr = %q, want a shutdown error message", stderr.String())
	}
	close(release) // unblock the handler so the client request resolves promptly
	<-resultCh     // drain the client goroutine (response or a reset, but not a 10s hang)
}

// TestServe_ListenerFailureReturnsExitServe asserts that a listener/bind failure
// (no signal involved) is reported and mapped to exitServe.
func TestServe_ListenerFailureReturnsExitServe(t *testing.T) {
	var stderr bytes.Buffer
	listen := func() error { return errors.New("listen tcp 127.0.0.1:8080: bind: address already in use") }
	got := serve(context.Background(), func() {}, &http.Server{}, listen, time.Second, &stderr)
	if got != exitServe {
		t.Fatalf("serve exit = %d, want exitServe (%d) on a listener failure", got, exitServe)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("serve:")) {
		t.Errorf("stderr = %q, want a serve error message", stderr.String())
	}
}

// TestServe_BenignServerClosedIsOK asserts that a benign http.ErrServerClosed from
// the listener (someone else closed the server) maps to exitOK, not a failure.
func TestServe_BenignServerClosedIsOK(t *testing.T) {
	var stderr bytes.Buffer
	listen := func() error { return http.ErrServerClosed }
	got := serve(context.Background(), func() {}, &http.Server{}, listen, time.Second, &stderr)
	if got != exitOK {
		t.Fatalf("serve exit = %d, want exitOK (%d) on ErrServerClosed", got, exitOK)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing for a benign close", stderr.String())
	}
}

// TestNewServer_Timeouts pins the slowloris guards on the constructed server. The
// critical assertion is ReadTimeout: without it ReadHeaderTimeout bounds only the
// header phase, leaving a body-trickle slowloris open on the only write endpoint.
func TestNewServer_Timeouts(t *testing.T) {
	srv := newServer("127.0.0.1:0", http.NewServeMux())
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q, want 127.0.0.1:0", srv.Addr)
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s (bounds the body phase; closes the body slowloris)", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", srv.IdleTimeout)
	}
}
