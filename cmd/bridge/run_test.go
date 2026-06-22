package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// captureLogs swaps the runner's logger for one that writes to the returned
// buffer so tests can assert on what was (or wasn't) logged.
func captureLogs(r *runner) *bytes.Buffer {
	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

func TestWarnIfSilentFetch_FiresOnEmptyFetchWithPriorHistory(t *testing.T) {
	// Setup: a channel where we've already seen messages. An empty fetch from
	// here onwards is suspicious — either Eitaa's markup changed or the channel
	// was wiped. Both warrant the operator's attention.
	r, _ := newTestRunner(t, &mockPub{})
	r.store.Mark("test", 1)
	buf := captureLogs(r)

	r.warnIfSilentFetch(nil)

	got := buf.String()
	if !strings.Contains(got, "Eitaa markup may have changed") {
		t.Errorf("expected silent-fetch warning, got log output: %q", got)
	}
	if !strings.Contains(got, `seen_count=1`) {
		t.Errorf("expected seen_count=1 in log line, got: %q", got)
	}
}

func TestWarnIfSilentFetch_QuietOnFirstRun(t *testing.T) {
	// On the very first run against a freshly created channel the seen-set is
	// empty and an empty fetch is legitimate. The warning must NOT fire here or
	// every bootstrap would produce a false alarm.
	r, _ := newTestRunner(t, &mockPub{})
	buf := captureLogs(r)

	r.warnIfSilentFetch(nil)

	if got := buf.String(); got != "" {
		t.Errorf("expected no log output on empty seen-set, got: %q", got)
	}
}

func TestWarnIfSilentFetch_QuietWhenMessagesPresent(t *testing.T) {
	// Normal operation: the fetch returned messages. The warning must stay quiet
	// regardless of how full the seen-set is.
	r, _ := newTestRunner(t, &mockPub{})
	r.store.Mark("test", 1)
	buf := captureLogs(r)

	msgs := []eitaa.Message{{ID: 99, Channel: "test", Date: time.Now()}}
	r.warnIfSilentFetch(msgs)

	if got := buf.String(); got != "" {
		t.Errorf("expected no log output when messages are present, got: %q", got)
	}
}
