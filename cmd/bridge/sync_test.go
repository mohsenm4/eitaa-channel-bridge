package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

// recPub captures Update/Delete calls so tests can assert sync behaviour without an HTTP target.
type recPub struct {
	mu      sync.Mutex
	nextID  int
	updates []updateCall
	deletes []int
}

type updateCall struct {
	postID int
	msg    router.Routed
}

func (p *recPub) Name() string { return "rec" }
func (p *recPub) Close() error { return nil }
func (p *recPub) Publish(context.Context, router.Routed) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	return 1000 + p.nextID, nil
}
func (p *recPub) Update(_ context.Context, postID int, msg router.Routed) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, updateCall{postID: postID, msg: msg})
	return nil
}
func (p *recPub) Delete(_ context.Context, postID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deletes = append(p.deletes, postID)
	return nil
}

func newSyncRunner(t *testing.T) (*runner, *recPub) {
	t.Helper()
	pub := &recPub{}
	r, _ := newTestRunner(t, &mockPub{})
	r.pub = pub
	r.cfg.Source.EditWatchWindow = time.Hour
	return r, pub
}

func TestSyncEdit_PushesUpdateOnFingerprintChange(t *testing.T) {
	r, pub := newSyncRunner(t)

	orig := eitaa.Message{
		ID: 100, Channel: "test",
		Text: "📌 hello\n\n#test", Date: time.Now(),
	}
	if !r.processOne(context.Background(), orig) {
		t.Fatal("seed publish failed")
	}

	edited := orig
	edited.Text = "📌 hello edited\n\n#test"
	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{edited})

	if len(pub.updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(pub.updates))
	}
	if pub.updates[0].msg.Title != "hello edited" {
		t.Errorf("update title = %q, want %q", pub.updates[0].msg.Title, "hello edited")
	}

	// A second tick with the same edited text must not retrigger Update (fp now matches stored).
	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{edited})
	if len(pub.updates) != 1 {
		t.Errorf("expected no further updates, got %d total", len(pub.updates))
	}
}

func TestSyncDelete_PushesDeleteWhenMissing(t *testing.T) {
	r, pub := newSyncRunner(t)

	a := eitaa.Message{ID: 100, Channel: "test", Text: "📌 a\n\n#test", Date: time.Now()}
	b := eitaa.Message{ID: 101, Channel: "test", Text: "📌 b\n\n#test", Date: time.Now()}
	r.processOne(context.Background(), a)
	r.processOne(context.Background(), b)

	// b vanished from the page; a still present.
	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{a})

	if len(pub.deletes) != 1 || pub.deletes[0] == 0 {
		t.Fatalf("expected 1 delete, got %v", pub.deletes)
	}

	// Re-tick: Deleted flag must suppress repeat deletion.
	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{a})
	if len(pub.deletes) != 1 {
		t.Errorf("expected delete to be one-shot, got %d total", len(pub.deletes))
	}
}

// Late edits (past EditWatchWindow) are still mirrored — the window only controls polling rate,
// not whether sync runs. Without this, a delete-then-edit at hour 7 would never reach WP.
func TestSyncEdit_LateEditStillApplied(t *testing.T) {
	r, pub := newSyncRunner(t)
	r.cfg.Source.EditWatchWindow = time.Minute

	old := eitaa.Message{
		ID: 100, Channel: "test",
		Text: "📌 old\n\n#test", Date: time.Now().Add(-2 * time.Hour),
	}
	r.processOne(context.Background(), old)

	edited := old
	edited.Text = "📌 old edited\n\n#test"
	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{edited})

	if len(pub.updates) != 1 {
		t.Errorf("expected 1 update for late edit, got %d", len(pub.updates))
	}
}

// Late deletions (past EditWatchWindow) must also still be propagated — Mohsen explicitly wants
// the next cold-mode tick to catch deletions made hours after the message was posted.
func TestSyncDelete_LateDeletionStillApplied(t *testing.T) {
	r, pub := newSyncRunner(t)
	r.cfg.Source.EditWatchWindow = time.Minute

	old := eitaa.Message{
		ID: 100, Channel: "test",
		Text: "📌 old\n\n#test", Date: time.Now().Add(-2 * time.Hour),
	}
	// Newer message so old's ID is inside the visible range after deletion.
	newer := eitaa.Message{
		ID: 99, Channel: "test",
		Text: "📌 newer\n\n#test", Date: time.Now().Add(-2 * time.Hour),
	}
	r.processOne(context.Background(), newer)
	r.processOne(context.Background(), old)

	r.syncEditsAndDeletes(context.Background(), []eitaa.Message{newer})

	if len(pub.deletes) != 1 {
		t.Errorf("expected 1 delete for late deletion, got %d", len(pub.deletes))
	}
}

func TestNextInterval_HotWhenRecent(t *testing.T) {
	r, _ := newSyncRunner(t)
	r.cfg.Source.HotPollInterval = 10 * time.Second
	r.cfg.Source.PollInterval = 6 * time.Hour

	recent := eitaa.Message{ID: 1, Channel: "test", Text: "📌 hi\n\n#test", Date: time.Now()}
	r.processOne(context.Background(), recent)

	if got := r.nextInterval(); got != 10*time.Second {
		t.Errorf("expected hot interval, got %s", got)
	}
}

func TestNextInterval_ColdWhenStale(t *testing.T) {
	r, _ := newSyncRunner(t)
	r.cfg.Source.HotPollInterval = 10 * time.Second
	r.cfg.Source.PollInterval = 6 * time.Hour
	r.cfg.Source.EditWatchWindow = time.Hour

	old := eitaa.Message{
		ID: 1, Channel: "test",
		Text: "📌 hi\n\n#test", Date: time.Now().Add(-2 * time.Hour),
	}
	r.processOne(context.Background(), old)

	if got := r.nextInterval(); got != 6*time.Hour {
		t.Errorf("expected cold interval, got %s", got)
	}
}
