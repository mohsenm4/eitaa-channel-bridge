package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// healthcheckRecorder fakes a healthchecks.io endpoint: records every GET path
// so tests can assert which variant (plain or /fail) was pinged.
type healthcheckRecorder struct {
	srv    *httptest.Server
	paths  []string
	mu     atomic.Bool
	failed atomic.Int32
}

func newHealthcheckRecorder() *healthcheckRecorder {
	rec := &healthcheckRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for !rec.mu.CompareAndSwap(false, true) {
		}
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Store(false)
		if strings.HasSuffix(r.URL.Path, "/fail") {
			rec.failed.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	return rec
}

func (rec *healthcheckRecorder) Close()         { rec.srv.Close() }
func (rec *healthcheckRecorder) URL() string    { return rec.srv.URL }
func (rec *healthcheckRecorder) FailPings() int { return int(rec.failed.Load()) }
func (rec *healthcheckRecorder) Paths() []string {
	for !rec.mu.CompareAndSwap(false, true) {
	}
	defer rec.mu.Store(false)
	return append([]string(nil), rec.paths...)
}
func (rec *healthcheckRecorder) wait(d time.Duration) { time.Sleep(d) }

func TestRecordPublishFailure_PingsFailAfterThreshold(t *testing.T) {
	rec := newHealthcheckRecorder()
	defer rec.Close()

	r, _ := newTestRunner(t, &mockPub{})
	r.cfg.Source.HealthcheckURL = rec.URL()
	r.cfg.Source.HealthcheckFailureThreshold = 3

	ctx := context.Background()
	r.recordPublishFailure(ctx)
	r.recordPublishFailure(ctx)
	if got := rec.FailPings(); got != 0 {
		t.Fatalf("after 2 failures want 0 /fail pings, got %d", got)
	}
	r.recordPublishFailure(ctx)
	rec.wait(50 * time.Millisecond)
	if got := rec.FailPings(); got != 1 {
		t.Fatalf("after 3 failures want 1 /fail ping, got %d (paths=%v)", got, rec.Paths())
	}

	// Further failures within the same streak must not spam the endpoint.
	r.recordPublishFailure(ctx)
	r.recordPublishFailure(ctx)
	rec.wait(50 * time.Millisecond)
	if got := rec.FailPings(); got != 1 {
		t.Fatalf("streak should ping /fail only once, got %d (paths=%v)", got, rec.Paths())
	}
}

func TestRecordPublishSuccess_ResetsStreak(t *testing.T) {
	rec := newHealthcheckRecorder()
	defer rec.Close()

	r, _ := newTestRunner(t, &mockPub{})
	r.cfg.Source.HealthcheckURL = rec.URL()
	r.cfg.Source.HealthcheckFailureThreshold = 2

	ctx := context.Background()
	r.recordPublishFailure(ctx)
	r.recordPublishSuccess()
	r.recordPublishFailure(ctx) // streak is back to 1 after the success
	rec.wait(30 * time.Millisecond)
	if got := rec.FailPings(); got != 0 {
		t.Fatalf("success should have reset the streak so /fail isn't pinged; got %d", got)
	}
	r.recordPublishFailure(ctx) // now at 2 → fire
	rec.wait(50 * time.Millisecond)
	if got := rec.FailPings(); got != 1 {
		t.Fatalf("want 1 /fail ping after threshold reached, got %d", got)
	}
}

func TestRecordPublishFailure_ThresholdZeroDisables(t *testing.T) {
	rec := newHealthcheckRecorder()
	defer rec.Close()

	r, _ := newTestRunner(t, &mockPub{})
	r.cfg.Source.HealthcheckURL = rec.URL()
	r.cfg.Source.HealthcheckFailureThreshold = 0

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		r.recordPublishFailure(ctx)
	}
	rec.wait(30 * time.Millisecond)
	if got := rec.FailPings(); got != 0 {
		t.Fatalf("threshold=0 disables; want 0 pings, got %d", got)
	}
}
