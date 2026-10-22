package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
)

// publishN publishes n messages starting at first and returns them as they appear on the channel page.
func publishN(t *testing.T, r *runner, first, n int) []eitaa.Message {
	t.Helper()
	var msgs []eitaa.Message
	for id := first; id < first+n; id++ {
		m := eitaa.Message{ID: id, Channel: "test", Text: fmt.Sprintf("📌 post %d\n\n#test", id), Date: time.Now()}
		if !r.processOne(context.Background(), m) {
			t.Fatalf("seed publish %d failed", id)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestSyncBrake_BlocksMassDeletion(t *testing.T) {
	r, pub := newSyncRunner(t)
	r.cfg.Source.SyncMaxDeletes = 3
	page := publishN(t, r, 100, 6)
	ctx := context.Background()

	// The page suddenly shows only the first post: five "deletions" at once looks like a misread page.
	r.syncEditsAndDeletes(ctx, page[:1])

	if len(pub.deletes) != 0 {
		t.Fatalf("brake should block every delete, got %v", pub.deletes)
	}
	for _, m := range page[1:] {
		if r.store.Get("test", m.ID).Deleted {
			t.Errorf("message %d marked deleted despite the brake", m.ID)
		}
	}

	// Next poll reads the page correctly again: nothing was lost.
	r.syncEditsAndDeletes(ctx, page)
	if len(pub.deletes) != 0 {
		t.Errorf("no post should be trashed once the page looks normal, got %v", pub.deletes)
	}
}

func TestSyncBrake_AllowsDeletesUpToLimit(t *testing.T) {
	// Locks normal behaviour: exactly the limit still goes through.
	r, pub := newSyncRunner(t)
	r.cfg.Source.SyncMaxDeletes = 3
	page := publishN(t, r, 100, 4)

	r.syncEditsAndDeletes(context.Background(), page[:1])

	if len(pub.deletes) != 3 {
		t.Errorf("expected 3 deletes at the limit, got %v", pub.deletes)
	}
}

func TestSyncBrake_BlocksMassEditsAndKeepsFingerprints(t *testing.T) {
	r, pub := newSyncRunner(t)
	r.cfg.Source.SyncMaxEdits = 5
	page := publishN(t, r, 100, 6)
	edited := make([]eitaa.Message, len(page))
	for i, m := range page {
		m.Text = fmt.Sprintf("📌 post %d edited\n\n#test", m.ID)
		edited[i] = m
	}
	ctx := context.Background()

	r.syncEditsAndDeletes(ctx, edited)
	if len(pub.updates) != 0 {
		t.Fatalf("brake should block every update, got %d", len(pub.updates))
	}

	// Raising the limit (what the operator does for a real mass edit) lets the same edits through.
	r.cfg.Source.SyncMaxEdits = 10
	r.syncEditsAndDeletes(ctx, edited)
	if len(pub.updates) != 6 {
		t.Errorf("expected 6 updates after raising the limit, got %d", len(pub.updates))
	}
}

func TestSyncBrake_PingsFailOncePerEpisode(t *testing.T) {
	rec := newHealthcheckRecorder()
	defer rec.Close()
	r, _ := newSyncRunner(t)
	r.cfg.Source.HealthcheckURL = rec.URL()
	r.cfg.Source.SyncMaxDeletes = 1
	page := publishN(t, r, 100, 3)
	ctx := context.Background()

	r.syncEditsAndDeletes(ctx, page[:1])
	r.syncEditsAndDeletes(ctx, page[:1])
	if got := rec.FailPings(); got != 1 {
		t.Fatalf("expected one /fail ping while the brake stays engaged, got %d", got)
	}

	r.syncEditsAndDeletes(ctx, page)
	r.syncEditsAndDeletes(ctx, page[:1])
	if got := rec.FailPings(); got != 2 {
		t.Errorf("expected a new /fail ping for a new episode, got %d", got)
	}
}
